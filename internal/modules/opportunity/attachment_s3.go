package opportunity

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	s3UploadTTL        = 10 * time.Minute
	s3Algorithm        = "AWS4-HMAC-SHA256"
	s3Service          = "s3"
	s3SignedDateLayout = "20060102T150405Z"
	s3ShortDateLayout  = "20060102"
	s3MaxObjectMemory  = uint64(64 << 20)
)

// S3AttachmentOptions 描述 S3 兼容对象存储连接参数。endpoint 形如
// "https://s3.example.com" 或 "http://minio:9000"；pathStyle=true 时请求
// 路径为 /<bucket>/<key>（MinIO/OSS 常用），否则使用虚拟主机风格。
type S3AttachmentOptions struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
	// Prefix 是所有对象键的固定前缀，例如 "crm-attachments/"。
	Prefix string
	Client *http.Client
}

// S3AttachmentObjectStore 把商机附件写入任意 S3 兼容对象存储（AWS S3、
// MinIO、阿里云 OSS S3 端点等）。上传走 CRM 进程内代理（PutVerified），
// 对象键由服务端生成；终结时以 ETag 作为不可变对象版本，读取时通过
// If-Match 与流式摘要校验防止扫描通过后内容被替换。
type S3AttachmentObjectStore struct {
	endpoint        *url.URL
	region          string
	bucket          string
	accessKeyID     string
	secretAccessKey string
	pathStyle       bool
	prefix          string
	client          *http.Client
}

// NewS3AttachmentObjectStore 构造 S3 兼容附件存储并校验连接参数。
func NewS3AttachmentObjectStore(options S3AttachmentOptions) (*S3AttachmentObjectStore, error) {
	endpoint, err := url.Parse(strings.TrimSpace(options.Endpoint))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("attachment S3 endpoint must be an absolute URL")
	}
	if endpoint.Scheme != "https" && endpoint.Scheme != "http" {
		return nil, errors.New("attachment S3 endpoint scheme must be http or https")
	}
	region := strings.TrimSpace(options.Region)
	bucket := strings.TrimSpace(options.Bucket)
	if region == "" || bucket == "" || bucket == "." || strings.ContainsAny(bucket, "/@") {
		return nil, errors.New("attachment S3 region and bucket are required")
	}
	if strings.TrimSpace(options.AccessKeyID) == "" || strings.TrimSpace(options.SecretAccessKey) == "" {
		return nil, errors.New("attachment S3 credentials are required")
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	prefix := strings.Trim(strings.ReplaceAll(strings.TrimSpace(options.Prefix), "\\", "/"), "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3AttachmentObjectStore{
		endpoint:        endpoint,
		region:          region,
		bucket:          bucket,
		accessKeyID:     strings.TrimSpace(options.AccessKeyID),
		secretAccessKey: strings.TrimSpace(options.SecretAccessKey),
		pathStyle:       options.PathStyle,
		prefix:          prefix,
		client:          client,
	}, nil
}

func (s *S3AttachmentObjectStore) Available() bool { return s != nil && s.endpoint != nil }

// CreateUpload 返回进程内代理上传授权。与本地存储一致，浏览器把文件内容
// 直接交给 CRM API，由 PutVerified 校验摘要后写入对象存储；Signed-URL 直传
// 需要独立的回调凭据契约，未签版前不启用。
func (s *S3AttachmentObjectStore) CreateUpload(context.Context, string, string, uint64, string, string) (AttachmentUploadGrant, error) {
	if !s.Available() {
		return AttachmentUploadGrant{}, ErrAttachmentUnavailable
	}
	return AttachmentUploadGrant{URL: "internal://crm-attachment-upload", ExpiresAt: time.Now().UTC().Add(s3UploadTTL)}, nil
}

// PutVerified 校验声明的大小与摘要后把对象写入 S3，并在对象元数据中固化
// 信任属性（sha256/size/mime）。同键重放相同内容幂等成功，内容不一致失败。
func (s *S3AttachmentObjectStore) PutVerified(ctx context.Context, key string, body io.Reader, size uint64, digest, media string) error {
	if !s.Available() {
		return ErrAttachmentUnavailable
	}
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	if size == 0 || size > s3MaxObjectMemory {
		return ErrAttachmentInvalid
	}
	buffer := make([]byte, size)
	read, readErr := io.ReadFull(body, buffer)
	if readErr != nil && !(readErr == io.EOF || readErr == io.ErrUnexpectedEOF) {
		return ErrAttachmentInvalid
	}
	if uint64(read) != size {
		return ErrAttachmentInvalid
	}
	hash := sha256.Sum256(buffer)
	actual := hex.EncodeToString(hash[:])
	if !strings.EqualFold(actual, digest) {
		return ErrAttachmentInvalid
	}
	canonical := canonicalMIME(media)
	if canonical == "" {
		return ErrAttachmentInvalid
	}
	headers := map[string]string{
		"Content-Type":      canonical,
		"x-amz-meta-sha256": strings.ToLower(digest),
		"x-amz-meta-size":   strconv.FormatUint(size, 10),
		"x-amz-meta-mime":   canonical,
	}
	status, head, err := s.do(ctx, http.MethodPut, objectKey, bytes.NewReader(buffer), headers)
	if err != nil {
		return fmt.Errorf("put attachment object: %w", err)
	}
	defer drainAndClose(head)
	if status != http.StatusOK {
		return fmt.Errorf("put attachment object returned HTTP %d", status)
	}
	return nil
}

// Finalize 读取对象元数据并重新校验内容摘要，返回以 ETag 为版本的不可变引用。
func (s *S3AttachmentObjectStore) Finalize(ctx context.Context, key string) (AttachmentObjectMetadata, error) {
	if !s.Available() {
		return AttachmentObjectMetadata{}, ErrAttachmentUnavailable
	}
	objectKey, err := s.objectKey(key)
	if err != nil {
		return AttachmentObjectMetadata{}, err
	}
	status, head, err := s.do(ctx, http.MethodGet, objectKey, nil, nil)
	if err != nil {
		return AttachmentObjectMetadata{}, fmt.Errorf("read attachment object for finalize: %w", err)
	}
	defer drainAndClose(head)
	if status != http.StatusOK {
		return AttachmentObjectMetadata{}, fmt.Errorf("read attachment object for finalize returned HTTP %d", status)
	}
	etag := unquoteETag(head.Header.Get("ETag"))
	if etag == "" {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	declaredDigest := strings.ToLower(strings.TrimSpace(head.Header.Get("x-amz-meta-sha256")))
	declaredSize := strings.TrimSpace(head.Header.Get("x-amz-meta-size"))
	declaredMIME := canonicalMIME(head.Header.Get("x-amz-meta-mime"))
	if declaredDigest == "" || declaredSize == "" || declaredMIME == "" {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	parsedSize, parseErr := strconv.ParseUint(declaredSize, 10, 64)
	if parseErr != nil || parsedSize == 0 {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(hasher, head.Body)
	if copyErr != nil {
		return AttachmentObjectMetadata{}, fmt.Errorf("verify attachment object digest: %w", copyErr)
	}
	if uint64(written) != parsedSize {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, declaredDigest) {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	return AttachmentObjectMetadata{ObjectVersion: etag, SizeBytes: parsedSize, MIMEType: declaredMIME, SHA256: actual}, nil
}

// OpenVerified 通过 If-Match 绑定 ETag 版本，并在流式读取结束时校验摘要；
// 任何版本漂移（HTTP 412）、大小不符或摘要不匹配都会失败关闭。
func (s *S3AttachmentObjectStore) OpenVerified(ctx context.Context, key, version, digest string, size uint64) (io.ReadCloser, error) {
	if !s.Available() {
		return nil, ErrAttachmentUnavailable
	}
	objectKey, err := s.objectKey(key)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	if strings.TrimSpace(version) != "" {
		headers["If-Match"] = `"` + strings.Trim(strings.TrimSpace(version), `"`) + `"`
	}
	status, response, err := s.do(ctx, http.MethodGet, objectKey, nil, headers)
	if err != nil {
		return nil, fmt.Errorf("open attachment object: %w", err)
	}
	if status != http.StatusOK {
		drainAndClose(response)
		if status == http.StatusPreconditionFailed || status == http.StatusNotFound {
			return nil, ErrAttachmentInvalid
		}
		return nil, fmt.Errorf("open attachment object returned HTTP %d", status)
	}
	contentLength := strings.TrimSpace(response.Header.Get("Content-Length"))
	if contentLength != "" {
		parsed, parseErr := strconv.ParseUint(contentLength, 10, 64)
		if parseErr != nil || parsed != size {
			drainAndClose(response)
			return nil, ErrAttachmentInvalid
		}
	}
	return newVerifyingReader(response.Body, digest, size), nil
}

func (s *S3AttachmentObjectStore) objectKey(key string) (string, error) {
	clean := strings.Trim(strings.ReplaceAll(strings.TrimSpace(key), "\\", "/"), "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "../") {
		return "", ErrAttachmentInvalid
	}
	return s.prefix + clean, nil
}

// do 组装并签名一个 S3 请求。body 为 nil 时表示 GET/HEAD；payloadHash 由
// 调用方传入的 reader 在签名前不可知，因此 PUT 使用已知缓冲区的摘要。
func (s *S3AttachmentObjectStore) do(ctx context.Context, method, objectKey string, body io.Reader, extraHeaders map[string]string) (int, *http.Response, error) {
	now := time.Now().UTC()
	signedDate := now.Format(s3SignedDateLayout)
	shortDate := now.Format(s3ShortDateLayout)
	payloadHash := emptyPayloadHash
	var payload []byte
	if body != nil {
		buffered, readErr := io.ReadAll(body)
		if readErr != nil {
			return 0, nil, readErr
		}
		payload = buffered
		digest := sha256.Sum256(buffered)
		payloadHash = hex.EncodeToString(digest[:])
	}
	requestURL, path, err := s.requestURL(objectKey)
	if err != nil {
		return 0, nil, err
	}
	signedHeaders, canonicalHeaders := s.canonicalHeaders(requestURL.Host, extraHeaders, payloadHash, signedDate)
	canonicalRequest := strings.Join([]string{method, path, "", canonicalHeaders, signedHeaders, payloadHash}, "\n")
	scope := strings.Join([]string{shortDate, s.region, s3Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{s3Algorithm, signedDate, scope, hex.EncodeToString(hashSHA256([]byte(canonicalRequest)))}, "\n")
	signature := hex.EncodeToString(hmacSHA256(s.signingKey(shortDate), []byte(stringToSign)))
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	for name, value := range extraHeaders {
		request.Header.Set(name, value)
	}
	request.Header.Set("x-amz-date", signedDate)
	request.Header.Set("x-amz-content-sha256", payloadHash)
	if body != nil {
		request.ContentLength = int64(len(payload))
	}
	request.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s3Algorithm, s.accessKeyID, scope, signedHeaders, signature,
	))
	response, err := s.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, response, nil
}

func (s *S3AttachmentObjectStore) requestURL(objectKey string) (*url.URL, string, error) {
	host := s.endpoint.Host
	path := "/" + s3URIEncodePath(objectKey)
	if s.pathStyle {
		path = "/" + s3URIEncodePath(s.bucket) + path
	} else {
		host = s.bucket + "." + host
	}
	target, err := url.Parse(strings.TrimRight(s.endpoint.Scheme+"://"+host, "/") + path)
	if err != nil {
		return nil, "", err
	}
	return target, path, nil
}

func (s *S3AttachmentObjectStore) canonicalHeaders(host string, extra map[string]string, payloadHash, signedDate string) (string, string) {
	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           signedDate,
	}
	for name, value := range extra {
		headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteString(":")
		builder.WriteString(headers[name])
		builder.WriteString("\n")
	}
	return strings.Join(names, ";"), builder.String()
}

func (s *S3AttachmentObjectStore) signingKey(shortDate string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+s.secretAccessKey), []byte(shortDate))
	regionKey := hmacSHA256(dateKey, []byte(s.region))
	serviceKey := hmacSHA256(regionKey, []byte(s3Service))
	return hmacSHA256(serviceKey, []byte("aws4_request"))
}

var emptyPayloadHash = func() string {
	digest := sha256.Sum256(nil)
	return hex.EncodeToString(digest[:])
}()

func hashSHA256(payload []byte) []byte {
	digest := sha256.Sum256(payload)
	return digest[:]
}

func hmacSHA256(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil)
}

func s3URIEncodePath(value string) string {
	var builder strings.Builder
	for _, char := range value {
		character := byte(char)
		switch {
		case character >= 'A' && character <= 'Z', character >= 'a' && character <= 'z',
			character >= '0' && character <= '9', character == '/', character == '-',
			character == '.', character == '_', character == '~':
			builder.WriteByte(character)
		default:
			builder.WriteString(fmt.Sprintf("%%%02X", character))
		}
	}
	return builder.String()
}

func unquoteETag(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func drainAndClose(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	_ = response.Body.Close()
}

// verifyingReader 在流式读取结束时校验总字节数与 SHA-256 摘要；Close 时
// 内容未读完整或摘要不匹配都会返回 ErrAttachmentInvalid。
type verifyingReader struct {
	body     io.ReadCloser
	hash     interface {
		io.Writer
		Sum([]byte) []byte
	}
	expectedDigest string
	expectedSize   uint64
	read           uint64
	verified       bool
	verifyErr      error
}

func newVerifyingReader(body io.ReadCloser, digest string, size uint64) *verifyingReader {
	return &verifyingReader{body: body, hash: sha256.New(), expectedDigest: strings.ToLower(strings.TrimSpace(digest)), expectedSize: size}
}

func (r *verifyingReader) Read(buffer []byte) (int, error) {
	read, err := r.body.Read(buffer)
	if read > 0 {
		r.read += uint64(read)
		if _, writeErr := r.hash.Write(buffer[:read]); writeErr != nil {
			r.verifyErr = ErrAttachmentInvalid
			return read, writeErr
		}
	}
	if errors.Is(err, io.EOF) {
		r.verified = true
	}
	return read, err
}

func (r *verifyingReader) Close() error {
	closeErr := r.body.Close()
	if r.verifyErr == nil && (!r.verified || r.read != r.expectedSize || hex.EncodeToString(r.hash.Sum(nil)) != r.expectedDigest) {
		r.verifyErr = ErrAttachmentInvalid
	}
	if r.verifyErr != nil {
		return r.verifyErr
	}
	return closeErr
}
