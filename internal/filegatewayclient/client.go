// Package filegatewayclient 提供 CRM 与客户门户访问独立 File Gateway 的本地 HTTP 边界。
// 该包不导入平台内部实现，现有本地/S3 存储路径可继续运行并按功能逐步切换。
package filegatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
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
