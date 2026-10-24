// Package filegatewayclient 提供 CRM 与客户门户访问独立 文件网关 的本地 HTTP 边界。
// 该包不导入平台内部实现，现有本地/S3 存储路径可继续运行并按功能逐步切换。
package filegatewayclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxUploadBytes int64 = 20 << 20

// TokenSource 返回只具备文件上传/绑定权限的机器 Bearer Token。
type TokenSource func(context.Context) (string, error)

// Client 是 CRM/Portal 自有的 文件网关 HTTP 适配器，不改变现有存储接口。
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      TokenSource
}

// V2UploadInput 包含单次 v2 上传所需的完整服务端授权业务绑定。
// 调用方必须在构造该值前校验资源 ACL。
type V2UploadInput struct {
	RequestID, Purpose, Classification, ActorUserID    string
	Name, MediaType                                    string
	ResourceType, ResourceID, BindingType, DisplayName string
	SizeBytes                                          uint64
	SHA256                                             string
	Content                                            io.Reader
}

type V2UploadReceipt struct {
	FileID, BindingID, SHA256 string
	SizeBytes                 uint64
}

type V2DirectGrant struct {
	UploadID, FileID, Status, UploadURL, Ticket string
	ExpiresAt                                   time.Time
}

// New 校验网关 HTTP(S) origin 和令牌提供器。
func New(baseURL string, httpClient *http.Client, token TokenSource) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("file gateway base URL must be an HTTP(S) origin")
	}
	if token == nil {
		return nil, errors.New("file gateway token source is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	// 机器凭据和上传票据均绑定源站。禁止通过
	// HTTP 重定向将后台操作转移到其他目的地。
	boundClient := *httpClient
	boundClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: baseURL, httpClient: &boundClient, token: token}, nil
}

// Upload 上传受限大小的文件并返回独立网关生成的 file_id。requestID 用于完整请求哈希幂等。
func (client *Client) Upload(ctx context.Context, requestID, applicationID, classification, name, mediaType string, content io.Reader) (string, error) {
	return client.UploadForPurpose(ctx, requestID, applicationID, classification, "", name, mediaType, content)
}

// UploadForPurpose 让网关从服务端策略目录解析存储命名空间，客户端不传物理路径。
func (client *Client) UploadForPurpose(ctx context.Context, requestID, applicationID, classification, purpose, name, mediaType string, content io.Reader) (string, error) {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(applicationID) == "" || strings.TrimSpace(name) == "" || content == nil {
		return "", errors.New("request ID, application ID, file name and content are required")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("application_id", applicationID); err != nil {
		return "", err
	}
	if err := writer.WriteField("classification", classification); err != nil {
		return "", err
	}
	if strings.TrimSpace(purpose) != "" {
		if err := writer.WriteField("purpose", purpose); err != nil {
			return "", err
		}
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": name})},
		"Content-Type":        {mediaType},
	})
	if err != nil {
		return "", fmt.Errorf("create multipart file: %w", err)
	}
	written, err := io.Copy(part, io.LimitReader(content, maxUploadBytes+1))
	if err != nil {
		return "", fmt.Errorf("read upload content: %w", err)
	}
	if written > maxUploadBytes {
		return "", errors.New("upload exceeds 20 MiB")
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	var response struct {
		Data struct {
			FileID string `json:"file_id"`
		} `json:"data"`
	}
	if err = client.do(ctx, http.MethodPost, "/api/v1/files", requestID, writer.FormDataContentType(), &body, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.Data.FileID) == "" {
		return "", errors.New("file gateway response is missing file_id")
	}
	return response.Data.FileID, nil
}

// Bind 将 READY 文件绑定到调用方已完成租户归属校验的业务资源。
func (client *Client) Bind(ctx context.Context, requestID, applicationID, fileID, resourceType, resourceID, bindingType, displayName string) error {
	payload, err := json.Marshal(map[string]any{"application_id": applicationID, "resource_type": resourceType, "resource_id": resourceID, "binding_type": bindingType, "display_name": displayName})
	if err != nil {
		return err
	}
	return client.do(ctx, http.MethodPost, "/api/v1/files/"+url.PathEscape(fileID)+"/bindings", requestID, "application/json", bytes.NewReader(payload), nil)
}

// UploadV2 通过一次性网关票据写入受限数据流。受保护的本地暂存
// 文件仅用于在创建上传会话前确定不可变的大小和摘要。
func (client *Client) UploadV2(ctx context.Context, input V2UploadInput) (V2UploadReceipt, error) {
	if strings.TrimSpace(input.RequestID) == "" || strings.TrimSpace(input.Purpose) == "" || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.MediaType) == "" || strings.TrimSpace(input.ResourceType) == "" || strings.TrimSpace(input.ResourceID) == "" || strings.TrimSpace(input.BindingType) == "" || input.Content == nil {
		return V2UploadReceipt{}, errors.New("v2 file upload input is incomplete")
	}
	temporary, err := os.CreateTemp("", "crm-file-gateway-*.uploading")
	if err != nil {
		return V2UploadReceipt{}, errors.New("create protected upload staging file")
	}
	name := temporary.Name()
	defer func() { _ = temporary.Close(); _ = os.Remove(name) }()
	if err = temporary.Chmod(0o600); err != nil {
		return V2UploadReceipt{}, errors.New("protect upload staging file")
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(input.Content, maxUploadBytes+1))
	if err != nil || size <= 0 || size > maxUploadBytes {
		return V2UploadReceipt{}, errors.New("file must be between 1 byte and 20 MiB")
	}
	if err = temporary.Sync(); err != nil {
		return V2UploadReceipt{}, errors.New("flush upload staging file")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	input.SizeBytes, input.SHA256 = uint64(size), digest
	grant, err := client.CreateV2DirectUpload(ctx, input)
	if err != nil {
		return V2UploadReceipt{}, err
	}
	if grant.Status == "READY" {
		// 幂等会话已完成：上次响应可能丢失，重放时不能再申请票据或重复上传。
		return V2UploadReceipt{FileID: grant.FileID, SHA256: digest, SizeBytes: uint64(size)}, nil
	}
	if grant.Status != "CREATED" || grant.Ticket == "" {
		return V2UploadReceipt{}, errors.New("file gateway upload session is not writable")
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		return V2UploadReceipt{}, errors.New("rewind upload staging file")
	}
	uploadPath := strings.TrimPrefix(grant.UploadURL, "/file-gateway")
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, client.baseURL+uploadPath, temporary)
	if err != nil {
		return V2UploadReceipt{}, err
	}
	request.ContentLength = size
	request.Header.Set("Authorization", "UploadTicket "+grant.Ticket)
	request.Header.Set("Content-Type", input.MediaType)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return V2UploadReceipt{}, fmt.Errorf("file gateway content upload: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return V2UploadReceipt{}, fmt.Errorf("file gateway returned HTTP %d", response.StatusCode)
	}
	var completed struct {
		Data struct {
			BindingID string `json:"binding_id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&completed); err != nil {
		return V2UploadReceipt{}, fmt.Errorf("decode file gateway upload response: %w", err)
	}
	return V2UploadReceipt{FileID: grant.FileID, BindingID: completed.Data.BindingID, SHA256: digest, SizeBytes: uint64(size)}, nil
}

func (client *Client) createV2Session(ctx context.Context, input V2UploadInput) (V2DirectGrant, error) {
	if strings.TrimSpace(input.RequestID) == "" || strings.TrimSpace(input.Purpose) == "" || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.MediaType) == "" || strings.TrimSpace(input.ResourceType) == "" || strings.TrimSpace(input.ResourceID) == "" || strings.TrimSpace(input.BindingType) == "" || input.SizeBytes == 0 || strings.TrimSpace(input.SHA256) == "" {
		return V2DirectGrant{}, errors.New("v2 file upload input is incomplete")
	}
	payload, err := json.Marshal(map[string]any{
		"purpose": input.Purpose, "original_name": input.Name, "media_type": input.MediaType,
		"size_bytes": input.SizeBytes, "sha256": input.SHA256, "classification": input.Classification,
		"actor_user_id": input.ActorUserID, "resource_type": input.ResourceType, "resource_id": input.ResourceID,
		"binding_type": input.BindingType, "display_name": input.DisplayName, "idempotency_key": input.RequestID,
	})
	if err != nil {
		return V2DirectGrant{}, err
	}
	var session struct {
		Data struct {
			UploadID  string `json:"upload_id"`
			FileID    string `json:"file_id"`
			Status    string `json:"status"`
			UploadURL string `json:"upload_url"`
		} `json:"data"`
	}
	if err = client.do(ctx, http.MethodPost, "/api/v2/upload-sessions", input.RequestID, "application/json", bytes.NewReader(payload), &session); err != nil {
		return V2DirectGrant{}, err
	}
	if session.Data.UploadID == "" || session.Data.FileID == "" {
		return V2DirectGrant{}, errors.New("file gateway session response is invalid")
	}
	return V2DirectGrant{UploadID: session.Data.UploadID, FileID: session.Data.FileID, Status: session.Data.Status, UploadURL: session.Data.UploadURL}, nil
}

// CreateV2DirectUpload 创建幂等会话，并签署一次浏览器上传请求。
func (client *Client) CreateV2DirectUpload(ctx context.Context, input V2UploadInput) (V2DirectGrant, error) {
	grant, err := client.createV2Session(ctx, input)
	if err != nil || grant.Status != "CREATED" {
		return grant, err
	}
	var ticket struct {
		Data struct {
			Ticket    string `json:"ticket"`
			UploadURL string `json:"upload_url"`
			ExpiresAt string `json:"expires_at"`
		} `json:"data"`
	}
	if err = client.do(ctx, http.MethodPost, "/api/v2/upload-sessions/"+url.PathEscape(grant.UploadID)+"/tickets", input.RequestID, "application/json", nil, &ticket); err != nil {
		return V2DirectGrant{}, err
	}
	if ticket.Data.Ticket == "" || ticket.Data.UploadURL == "" {
		return V2DirectGrant{}, errors.New("file gateway ticket response is invalid")
	}
	grant.Ticket, grant.UploadURL = ticket.Data.Ticket, ticket.Data.UploadURL
	grant.ExpiresAt, err = time.Parse(time.RFC3339Nano, ticket.Data.ExpiresAt)
	if err != nil {
		return V2DirectGrant{}, errors.New("file gateway ticket expiry is invalid")
	}
	return grant, nil
}

// ResolveV2Upload 重放不可变的幂等创建请求，返回持久化会话。
func (client *Client) ResolveV2Upload(ctx context.Context, input V2UploadInput) (V2DirectGrant, error) {
	return client.createV2Session(ctx, input)
}

// OpenV2File 在调用方重新校验业务 ACL 后获取短期下载票据。
func (client *Client) OpenV2File(ctx context.Context, requestID, fileID, resourceType, resourceID string) (io.ReadCloser, error) {
	payload, err := json.Marshal(map[string]string{"resource_type": resourceType, "resource_id": resourceID})
	if err != nil {
		return nil, err
	}
	var grant struct {
		Data struct {
			DownloadURL, Ticket string `json:"-"`
		} `json:"data"`
	}
	// 匿名嵌入结构体字段不能携带独立标签，因此显式解码。
	var raw struct {
		Data struct {
			DownloadURL string `json:"download_url"`
			Ticket      string `json:"ticket"`
		} `json:"data"`
	}
	if err = client.do(ctx, http.MethodPost, "/api/v2/files/"+url.PathEscape(fileID)+"/download-tickets", requestID, "application/json", bytes.NewReader(payload), &raw); err != nil {
		return nil, err
	}
	grant.Data.DownloadURL, grant.Data.Ticket = raw.Data.DownloadURL, raw.Data.Ticket
	if grant.Data.DownloadURL == "" || grant.Data.Ticket == "" {
		return nil, errors.New("file gateway download ticket response is invalid")
	}
	path := strings.TrimPrefix(grant.Data.DownloadURL, "/file-gateway")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "DownloadTicket "+grant.Data.Ticket)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("file gateway download: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil, fmt.Errorf("file gateway returned HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func (client *Client) do(ctx context.Context, method, path, requestID, contentType string, body io.Reader, target any) error {
	token, err := client.token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return errors.New("obtain file gateway bearer token")
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", strings.TrimSpace(requestID))
	request.Header.Set("Idempotency-Key", strings.TrimSpace(requestID))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", contentType)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("file gateway request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("file gateway returned HTTP %d", response.StatusCode)
	}
	if target != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
			return fmt.Errorf("decode file gateway response: %w", err)
		}
	}
	return nil
}

// PortalReportSourceInput 标识已验证报告回调中的
// 精确不可变描述。委托网关端点重新检查源命名空间
// 及该业务绑定；Portal 不持有 Project 文件凭据。
type PortalReportSourceInput struct {
	RequestID      uint64 `json:"request_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ExpectedSize   int64  `json:"expected_size"`
	ExpectedMIME   string `json:"expected_mime"`
	ExpectedName   string `json:"expected_name"`
}
type PortalReportFile struct {
	VersionID, SHA256, MIME string
	Size                    int64
	Content                 io.ReadCloser
}

func (client *Client) OpenPortalReportFile(ctx context.Context, traceID, fileID string, input PortalReportSourceInput) (PortalReportFile, error) {
	empty := PortalReportFile{}
	if input.RequestID == 0 || input.ExpectedSize <= 0 || input.ExpectedSize > 50<<20 || input.ExpectedMIME != "application/pdf" || len(input.ExpectedSHA256) != 64 || input.ExpectedName == "" {
		return empty, errors.New("invalid report source descriptor")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return empty, err
	}
	token, err := client.token(ctx)
	if err != nil || token == "" {
		return empty, errors.New("report source authentication unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/api/v2/portal-report-files/"+url.PathEscape(fileID)+"/content", bytes.NewReader(body))
	if err != nil {
		return empty, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", traceID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/pdf")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return empty, errors.New("report source gateway unavailable")
	}
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	version := response.Header.Get("X-File-Version-ID")
	digest := response.Header.Get("X-File-SHA256")
	if response.StatusCode != http.StatusOK || mediaErr != nil || media != input.ExpectedMIME || response.ContentLength != input.ExpectedSize || !strings.EqualFold(digest, input.ExpectedSHA256) || version == "" || len(version) > 256 || strings.ContainsAny(version, "\x00\r\n") {
		_ = response.Body.Close()
		return empty, errors.New("report source gateway evidence invalid")
	}
	return PortalReportFile{VersionID: version, SHA256: digest, MIME: media, Size: response.ContentLength, Content: response.Body}, nil
}
