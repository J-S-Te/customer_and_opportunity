package opportunity

import (
	"context"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/clamav"
)

// fakeClamdForScanner 与 clamav 包测试中的假服务器行为一致，这里通过
// 真实 TCP 连接验证 ClamAVAttachmentScanner 的组合判定逻辑。
func startClamdForAttachmentTest(t *testing.T, verdict string) (*clamav.Client, func()) {
	t.Helper()
	daemon, address := startInlineClamd(t, verdict)
	client, err := clamav.NewClient("tcp", address)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, daemon
}

func TestClamAVAttachmentScannerClean(t *testing.T) {
	client, stop := startClamdForAttachmentTest(t, "stream: OK")
	defer stop()
	store, err := NewLocalAttachmentObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	scanner, err := NewClamAVAttachmentScanner(store, client, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClamAVAttachmentScanner: %v", err)
	}
	if !scanner.Available() {
		t.Fatal("scanner should be available")
	}
	content, digest, size, media := testAttachmentContent()
	if err = store.PutVerified(context.Background(), "obj", newByteReader(content), size, digest, media); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}
	metadata, err := store.Finalize(context.Background(), "obj")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	reference, err := scanner.Submit(context.Background(), "idem-1", "obj", metadata.ObjectVersion, digest, size, media)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	status, ok := scanner.ImmediateStatus(reference)
	if !ok || status != AttachmentClean {
		t.Fatalf("expected CLEAN from reference %q, got %q ok=%v", reference, status, ok)
	}
}

func TestClamAVAttachmentScannerInfected(t *testing.T) {
	client, stop := startClamdForAttachmentTest(t, "stream: EICAR-Test-File")
	defer stop()
	store, _ := NewLocalAttachmentObjectStore(t.TempDir())
	scanner, _ := NewClamAVAttachmentScanner(store, client, 5*time.Second)
	content, digest, size, media := testAttachmentContent()
	_ = store.PutVerified(context.Background(), "obj", newByteReader(content), size, digest, media)
	metadata, _ := store.Finalize(context.Background(), "obj")
	reference, err := scanner.Submit(context.Background(), "idem-1", "obj", metadata.ObjectVersion, digest, size, media)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	status, ok := scanner.ImmediateStatus(reference)
	if !ok || status != AttachmentRejected {
		t.Fatalf("expected REJECTED from reference %q, got %q ok=%v", reference, status, ok)
	}
}

func TestClamAVAttachmentScannerEngineUnavailable(t *testing.T) {
	// 不启动 clamd：客户端指向一个不存在的端口，扫描必须失败而不是放行。
	client, err := clamav.NewClient("tcp", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	store, _ := NewLocalAttachmentObjectStore(t.TempDir())
	scanner, _ := NewClamAVAttachmentScanner(store, client, 2*time.Second)
	content, digest, size, media := testAttachmentContent()
	_ = store.PutVerified(context.Background(), "obj", newByteReader(content), size, digest, media)
	metadata, _ := store.Finalize(context.Background(), "obj")
	if _, err = scanner.Submit(context.Background(), "idem-1", "obj", metadata.ObjectVersion, digest, size, media); err == nil {
		t.Fatal("expected engine-unavailable error, got success")
	}
}

func TestClamAVAttachmentScannerStaticRejection(t *testing.T) {
	// 内容含可执行头：即便 ClamAV 判定 OK，静态校验也必须拒绝。
	client, stop := startClamdForAttachmentTest(t, "stream: OK")
	defer stop()
	store, _ := NewLocalAttachmentObjectStore(t.TempDir())
	scanner, _ := NewClamAVAttachmentScanner(store, client, 5*time.Second)
	content := append([]byte("MZ"), []byte("fake executable payload padded to look like a real file")...)
	digest := sha256Hex(content)
	size := uint64(len(content))
	_ = store.PutVerified(context.Background(), "obj", newByteReader(content), size, digest, "application/pdf")
	metadata, _ := store.Finalize(context.Background(), "obj")
	reference, err := scanner.Submit(context.Background(), "idem-1", "obj", metadata.ObjectVersion, digest, size, "application/pdf")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	status, ok := scanner.ImmediateStatus(reference)
	if !ok || status != AttachmentRejected {
		t.Fatalf("expected static REJECTED, got %q ok=%v", status, ok)
	}
}
