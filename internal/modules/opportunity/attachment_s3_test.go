package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeS3 模拟 S3 兼容后端的最小语义：PUT 存储（含元数据与 ETag）、
// GET 支持 If-Match、对象一旦写入不可覆盖。
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]fakeS3Object
	server  *httptest.Server
}

type fakeS3Object struct {
	body     []byte
	etag     string
	metadata map[string]string
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	backend := &fakeS3{objects: map[string]fakeS3Object{}}
	backend.server = httptest.NewServer(http.HandlerFunc(backend.handle))
	t.Cleanup(backend.server.Close)
	return backend
}

func (b *fakeS3) handle(writer http.ResponseWriter, request *http.Request) {
	key := strings.TrimPrefix(request.URL.Path, "/test-bucket/")
	b.mu.Lock()
	defer b.mu.Unlock()
	switch request.Method {
	case http.MethodPut:
		if _, exists := b.objects[key]; exists {
			writer.WriteHeader(http.StatusConflict)
			return
		}
		body, _ := io.ReadAll(request.Body)
		digest := sha256.Sum256(body)
		b.objects[key] = fakeS3Object{
			body:     body,
			etag:     hex.EncodeToString(digest[:16]),
			metadata: map[string]string{
				"x-amz-meta-sha256": request.Header.Get("x-amz-meta-sha256"),
				"x-amz-meta-size":   request.Header.Get("x-amz-meta-size"),
				"x-amz-meta-mime":   request.Header.Get("x-amz-meta-mime"),
			},
		}
		writer.Header().Set("ETag", `"`+hex.EncodeToString(digest[:16])+`"`)
		writer.WriteHeader(http.StatusOK)
	case http.MethodGet:
		object, exists := b.objects[key]
		if !exists {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if match := request.Header.Get("If-Match"); match != "" && strings.Trim(match, `"`) != object.etag {
			writer.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		for name, value := range object.metadata {
			writer.Header().Set(name, value)
		}
		writer.Header().Set("ETag", `"`+object.etag+`"`)
		writer.Header().Set("Content-Length", strings.TrimSpace(itoa(len(object.body))))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(object.body)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func newTestS3Store(t *testing.T, backend *fakeS3) *S3AttachmentObjectStore {
	t.Helper()
	store, err := NewS3AttachmentObjectStore(S3AttachmentOptions{
		Endpoint: backend.server.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKeyID: "test-access-key", SecretAccessKey: "test-secret-key",
		PathStyle: true, Prefix: "crm-attachments",
	})
	if err != nil {
		t.Fatalf("NewS3AttachmentObjectStore: %v", err)
	}
	return store
}

func TestS3StoreLifecycle(t *testing.T) {
	backend := newFakeS3(t)
	store := newTestS3Store(t, backend)
	if !store.Available() {
		t.Fatal("store should be available")
	}
	content, digest, size, media := testAttachmentContent()

	grant, err := store.CreateUpload(context.Background(), "key", "name.pdf", size, digest, media)
	if err != nil || grant.URL != "internal://crm-attachment-upload" {
		t.Fatalf("CreateUpload: %v %q", err, grant.URL)
	}
	if err = store.PutVerified(context.Background(), "tenant-a/obj-1", bytes.NewReader(content), size, digest, media); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}
	metadata, err := store.Finalize(context.Background(), "tenant-a/obj-1")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if metadata.SizeBytes != size || !strings.EqualFold(metadata.SHA256, digest) || metadata.MIMEType != media || metadata.ObjectVersion == "" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	reader, err := store.OpenVerified(context.Background(), "tenant-a/obj-1", metadata.ObjectVersion, digest, size)
	if err != nil {
		t.Fatalf("OpenVerified: %v", err)
	}
	downloaded, readErr := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil || readErr != nil {
		t.Fatalf("read/close: %v %v", readErr, closeErr)
	}
	if !bytes.Equal(downloaded, content) {
		t.Fatal("downloaded content mismatch")
	}
}

func TestS3StorePutVerifiedDigestMismatch(t *testing.T) {
	backend := newFakeS3(t)
	store := newTestS3Store(t, backend)
	content, digest, size, media := testAttachmentContent()
	wrongDigest := strings.Repeat("a", 64)
	if digest == wrongDigest {
		t.Skip("collision")
	}
	if err := store.PutVerified(context.Background(), "obj", bytes.NewReader(content), size, wrongDigest, media); err != ErrAttachmentInvalid {
		t.Fatalf("expected ErrAttachmentInvalid, got %v", err)
	}
	// 大小不符同样拒绝。
	if err := store.PutVerified(context.Background(), "obj", bytes.NewReader(content), size+1, digest, media); err != ErrAttachmentInvalid {
		t.Fatalf("expected ErrAttachmentInvalid for size mismatch, got %v", err)
	}
}

func TestS3StoreOpenVerifiedVersionDrift(t *testing.T) {
	backend := newFakeS3(t)
	store := newTestS3Store(t, backend)
	content, digest, size, media := testAttachmentContent()
	if err := store.PutVerified(context.Background(), "obj", bytes.NewReader(content), size, digest, media); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}
	metadata, err := store.Finalize(context.Background(), "obj")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	// 使用错误版本（ETag）读取必须失败关闭。
	if _, err = store.OpenVerified(context.Background(), "obj", "deadbeef", digest, size); err != ErrAttachmentInvalid {
		t.Fatalf("expected ErrAttachmentInvalid for version drift, got %v", err)
	}
	// 使用错误摘要读取必须在关闭时失败。
	reader, err := store.OpenVerified(context.Background(), "obj", metadata.ObjectVersion, strings.Repeat("b", 64), size)
	if err != nil {
		t.Fatalf("OpenVerified: %v", err)
	}
	_, _ = io.Copy(io.Discard, reader)
	if err = reader.Close(); err != ErrAttachmentInvalid {
		t.Fatalf("expected digest verification failure on close, got %v", err)
	}
}

func TestS3StoreRejectsPathTraversal(t *testing.T) {
	backend := newFakeS3(t)
	store := newTestS3Store(t, backend)
	content, digest, size, media := testAttachmentContent()
	if err := store.PutVerified(context.Background(), "../escape", bytes.NewReader(content), size, digest, media); err != ErrAttachmentInvalid {
		t.Fatalf("expected path traversal rejection, got %v", err)
	}
}

func TestNewS3AttachmentObjectStoreValidation(t *testing.T) {
	base := S3AttachmentOptions{Endpoint: "http://minio:9000", Region: "r", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"}
	if _, err := NewS3AttachmentObjectStore(base); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	invalid := []S3AttachmentOptions{
		{Endpoint: "not-a-url", Region: "r", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "ftp://x", Region: "r", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "http://x", Region: "", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "http://x", Region: "r", Bucket: "bad/bucket", AccessKeyID: "a", SecretAccessKey: "s"},
		{Endpoint: "http://x", Region: "r", Bucket: "b", AccessKeyID: "", SecretAccessKey: "s"},
		{Endpoint: "http://x", Region: "r", Bucket: "b", AccessKeyID: "a", SecretAccessKey: ""},
	}
	for _, options := range invalid {
		if _, err := NewS3AttachmentObjectStore(options); err == nil {
			t.Fatalf("expected rejection for %+v", options)
		}
	}
}

func TestS3URIEncodePath(t *testing.T) {
	if got := s3URIEncodePath("crm-attachments/tenant-a/obj-1.bin"); got != "crm-attachments/tenant-a/obj-1.bin" {
		t.Fatalf("unexpected encoding: %q", got)
	}
	if got := s3URIEncodePath("a b+c"); got != "a%20b%2Bc" {
		t.Fatalf("unexpected encoding: %q", got)
	}
}
