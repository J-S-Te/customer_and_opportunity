// Package filegatewayclient 提供 CRM 与客户门户访问独立 File Gateway 的本地 HTTP 边界。
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

// Client 是 CRM/Portal 自有的 File Gateway HTTP 适配器，不改变现有存储接口。
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      TokenSource
}

// V2UploadInput is the complete server-authorized business binding for one v2 upload.
// Callers must validate the resource ACL before constructing this value.
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
	return &Client{baseURL: baseURL, httpClient: httpClient, token: token}, nil
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

// UploadV2 writes a bounded stream through a one-time gateway ticket. A protected local staging
// file is used only to determine the immutable size/digest before the upload session is created.
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
		Data struct{ UploadID, FileID, Status, UploadURL string } `json:"data"`
	}
	if err = client.do(ctx, http.MethodPost, "/api/v2/upload-sessions", input.RequestID, "application/json", bytes.NewReader(payload), &session); err != nil {
		return V2DirectGrant{}, err
	}
	if session.Data.UploadID == "" || session.Data.FileID == "" {
		return V2DirectGrant{}, errors.New("file gateway session response is invalid")
	}
	return V2DirectGrant{UploadID: session.Data.UploadID, FileID: session.Data.FileID, Status: session.Data.Status, UploadURL: session.Data.UploadURL}, nil
}

// CreateV2DirectUpload creates an idempotent session and signs one browser upload request.
func (client *Client) CreateV2DirectUpload(ctx context.Context, input V2UploadInput) (V2DirectGrant, error) {
	grant, err := client.createV2Session(ctx, input)
	if err != nil || grant.Status != "CREATED" {
		return grant, err
	}
	var ticket struct {
		Data struct{ Ticket, UploadURL, ExpiresAt string } `json:"data"`
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

// ResolveV2Upload replays the immutable idempotent create request and returns the durable session.
func (client *Client) ResolveV2Upload(ctx context.Context, input V2UploadInput) (V2DirectGrant, error) {
	return client.createV2Session(ctx, input)
}

// OpenV2File obtains a short-lived download ticket after the caller has rechecked its business ACL.
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
	// Anonymous embedded struct fields cannot carry independent tags, so decode explicitly.
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
