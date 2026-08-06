package presaleworker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type httpPorts struct {
	approvalClient *http.Client
	pmsClient      *http.Client
	approval       HTTPPortConfig
	pms            HTTPPortConfig
	now            func() time.Time
	nonceReader    io.Reader
}

func NewHTTPPorts(approval, pms HTTPPortConfig, allowInsecureHTTP bool) (presale.ApprovalCommandPort, presale.PMSPublisher, error) {
	if err := validatePort("approval", approval, true, allowInsecureHTTP); err != nil {
		return nil, nil, err
	}
	if err := validatePort("PMS", pms, false, allowInsecureHTTP); err != nil {
		return nil, nil, err
	}
	approvalClient, err := oauthClient(approval)
	if err != nil {
		return nil, nil, err
	}
	pmsClient, err := oauthClient(pms)
	if err != nil {
		return nil, nil, err
	}
	ports := &httpPorts{approvalClient: approvalClient, pmsClient: pmsClient, approval: approval, pms: pms, now: time.Now, nonceReader: rand.Reader}
	return ports, ports, nil
}

func oauthClient(cfg HTTPPortConfig) (*http.Client, error) {
	// 令牌和业务请求共用受控 TLS，且禁止重定向，避免 Authorization 头越过预配置服务边界。
	cc := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: cfg.TokenURL, Scopes: strings.Fields(cfg.Scope), AuthStyle: oauth2.AuthStyleInHeader}
	transport, err := integrationhttp.NewTransport(cfg.TLS, 2*time.Second)
	if err != nil {
		return nil, err
	}
	tokenHTTPClient := integrationClient(transport, 5*time.Second)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenHTTPClient)
	client := cc.Client(ctx)
	client.Timeout = 5 * time.Second
	client.CheckRedirect = rejectIntegrationRedirect
	return client, nil
}

func integrationClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: rejectIntegrationRedirect}
}

func rejectIntegrationRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func (p *httpPorts) Start(ctx context.Context, event presale.OutboxEvent) (presale.ApprovalStartResult, error) {
	type approvalStartData struct {
		EngineInstanceID string `json:"engine_instance_id"`
		EventSequence    uint64 `json:"event_sequence"`
	}
	response, err := p.postJSON(ctx, p.approvalClient, p.approval.StartURL, event.EventID, event.Payload)
	if err != nil {
		return presale.ApprovalStartResult{}, err
	}
	data, err := decodeSuccessEnvelope[approvalStartData](response, http.StatusOK, http.StatusCreated)
	if err != nil {
		return presale.ApprovalStartResult{}, fmt.Errorf("invalid approval start response")
	}
	result := presale.ApprovalStartResult{EngineInstanceID: data.EngineInstanceID, EventSequence: data.EventSequence}
	if !validIntegrationIdentity(result.EngineInstanceID) || result.EventSequence == 0 {
		return presale.ApprovalStartResult{}, fmt.Errorf("approval response is missing instance identity")
	}
	return result, nil
}

func (p *httpPorts) Act(ctx context.Context, event presale.OutboxEvent) error {
	var command struct {
		EngineTaskID string `json:"engine_task_id"`
	}
	if json.Unmarshal(event.Payload, &command) != nil || !validIntegrationIdentity(command.EngineTaskID) {
		return errors.New("approval action payload is missing task identity")
	}
	response, err := p.postJSON(ctx, p.approvalClient, p.approval.ActionURL, event.EventID, event.Payload)
	if err != nil {
		return err
	}
	type approvalActionData struct {
		EngineTaskID string `json:"engine_task_id"`
		Accepted     bool   `json:"accepted"`
	}
	data, err := decodeSuccessEnvelope[approvalActionData](response, http.StatusOK, http.StatusAccepted)
	if err != nil || !data.Accepted || data.EngineTaskID != command.EngineTaskID {
		return errors.New("invalid approval action response")
	}
	return nil
}

func (p *httpPorts) PublishWorklog(ctx context.Context, event presale.OutboxEvent) (string, error) {
	var command struct {
		WorklogID      string `json:"worklogId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if json.Unmarshal(event.Payload, &command) != nil || !validIntegrationIdentity(command.WorklogID) || command.IdempotencyKey != command.WorklogID {
		return "", errors.New("PMS worklog payload has an invalid business idempotency identity")
	}
	response, err := p.postJSON(ctx, p.pmsClient, p.pms.PublishURL, command.IdempotencyKey, event.Payload)
	if err != nil {
		return "", err
	}
	type worklogData struct {
		WorklogID   string `json:"worklog_id"`
		ReceiptCode string `json:"receipt_code"`
	}
	data, err := decodeSuccessEnvelope[worklogData](response, http.StatusOK, http.StatusAccepted)
	if err != nil || data.WorklogID != command.WorklogID || !validIntegrationIdentity(data.ReceiptCode) {
		return "", errors.New("invalid PMS worklog response")
	}
	return data.ReceiptCode, nil
}

type integrationResponse struct {
	status int
	body   []byte
}

type successEnvelope[T any] struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id"`
	Data      *T              `json:"data"`
	Details   json.RawMessage `json:"details,omitempty"`
}

func (p *httpPorts) postJSON(ctx context.Context, client *http.Client, endpoint, idempotencyKey string, payload []byte) (integrationResponse, error) {
	if client == nil || !validIntegrationIdentity(idempotencyKey) {
		return integrationResponse{}, errors.New("integration request is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return integrationResponse{}, err
	}
	nonce := make([]byte, 32)
	reader := p.nonceReader
	if reader == nil {
		reader = rand.Reader
	}
	if _, err = io.ReadFull(reader, nonce); err != nil {
		return integrationResponse{}, errors.New("generate integration nonce failed")
	}
	now := p.now
	if now == nil {
		now = time.Now
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	// 业务幂等键用于结果收敛；时间戳和随机 nonce 则限制同一认证请求被截获后的重放窗口。
	req.Header.Set("X-Integration-Timestamp", now().UTC().Format(time.RFC3339Nano))
	req.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	resp, err := client.Do(req)
	if err != nil {
		return integrationResponse{}, safeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		requestID := sanitizeHeader(resp.Header.Get("X-Request-ID"))
		if requestID != "" {
			return integrationResponse{}, fmt.Errorf("integration response status=%d request_id=%s", resp.StatusCode, requestID)
		}
		return integrationResponse{}, fmt.Errorf("integration response status=%d", resp.StatusCode)
	}
	if !isJSONContentType(resp.Header.Get("Content-Type")) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return integrationResponse{}, errors.New("invalid integration response content type")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return integrationResponse{}, errors.New("invalid integration response")
	}
	return integrationResponse{status: resp.StatusCode, body: body}, nil
}

func isJSONContentType(value string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return strings.EqualFold(mediaType, "application/json")
}

func decodeSuccessEnvelope[T any](response integrationResponse, statuses ...int) (T, error) {
	// 严格信封拒绝未知字段、尾随 JSON 和缺失请求 ID，防止错误服务的宽松响应被当作成功。
	var zero T
	allowed := false
	for _, status := range statuses {
		allowed = allowed || response.status == status
	}
	if !allowed {
		return zero, errors.New("unexpected integration response status")
	}
	decoder := json.NewDecoder(bytes.NewReader(response.body))
	decoder.DisallowUnknownFields()
	var envelope successEnvelope[T]
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Code != "OK" ||
		envelope.Data == nil || len(envelope.Details) != 0 || !validIntegrationIdentity(envelope.RequestID) {
		return zero, errors.New("invalid integration response envelope")
	}
	return *envelope.Data, nil
}

func validIntegrationIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r == ':' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

func safeTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("integration request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("integration request timed out")
	}
	return errors.New("integration transport failed")
}

func sanitizeHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return ""
		}
	}
	return value
}
