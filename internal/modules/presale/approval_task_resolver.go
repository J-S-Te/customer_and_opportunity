package presale

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	approvalTaskReadScope         = "presale.approval.task.read"
	maxApprovalTaskResponseBytes  = 64 << 10
	maxApprovalTaskIdentityLength = 128
)

type ApprovalTaskResolver interface {
	ResolveCurrentTask(context.Context, ApprovalTaskQuery) (ApprovalTask, error)
}

type ApprovalTaskQuery struct {
	TenantID, EngineInstanceID, ApproverID string
	Node                                   uint8
}

type ApprovalTask struct {
	EngineTaskID, EngineInstanceID, ApproverID string
	Node                                       uint8
}

type ApprovalTaskResolverOptions struct {
	Endpoint, TokenURL, ClientID, ClientSecret, Scope string
	TLS                                               integrationhttp.TLSOptions
	HTTPClient                                        *http.Client
	Now                                               func() time.Time
	NonceReader                                       io.Reader
}

type HTTPApprovalTaskResolver struct {
	endpoint    string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

// 解析器使用专用机器客户端和最小读取 scope 调用审批服务；只接受 HTTPS、禁止重定向，
// 并为每次请求附加时间戳、请求追踪号和随机 nonce，降低凭据误用与重放风险。
func NewHTTPApprovalTaskResolver(ctx context.Context, options ApprovalTaskResolverOptions) (*HTTPApprovalTaskResolver, error) {
	for name, value := range map[string]string{"endpoint": options.Endpoint, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("approval task %s is required", name)
		}
	}
	if options.Scope != approvalTaskReadScope || !validApprovalTaskURL(options.Endpoint) || !validApprovalTaskURL(options.TokenURL) {
		return nil, errors.New("approval task resolver configuration is invalid")
	}
	if err := options.TLS.ValidateEndpoints(options.TokenURL, options.Endpoint); err != nil {
		return nil, err
	}
	var transport http.RoundTripper
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		transport = options.HTTPClient.Transport
	} else {
		value, err := integrationhttp.NewTransport(options.TLS, 3*time.Second)
		if err != nil {
			return nil, err
		}
		transport = value
	}
	tokenClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: rejectApprovalTaskRedirect}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	credentials := clientcredentials.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, TokenURL: options.TokenURL, Scopes: []string{approvalTaskReadScope}, AuthStyle: oauth2.AuthStyleInHeader}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &HTTPApprovalTaskResolver{
		endpoint: options.Endpoint,
		client:   &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: transport}, Timeout: 10 * time.Second, CheckRedirect: rejectApprovalTaskRedirect},
		now:      now, nonceReader: nonceReader,
	}, nil
}

// 返回结果必须与请求中的实例、节点和审批人逐项一致且仍为 PENDING。
// 响应大小、媒体类型和 JSON 字段均严格校验，任何协议漂移都按依赖不可用失败关闭。
func (r *HTTPApprovalTaskResolver) ResolveCurrentTask(ctx context.Context, query ApprovalTaskQuery) (ApprovalTask, error) {
	if r == nil || r.client == nil || !validApprovalTaskIdentity(query.TenantID) || !validApprovalTaskIdentity(query.EngineInstanceID) || !validApprovalTaskIdentity(query.ApproverID) || query.Node < 1 || query.Node > 2 {
		return ApprovalTask{}, ErrDependencyUnavailable
	}
	values := url.Values{"approver_id": {query.ApproverID}, "engine_instance_id": {query.EngineInstanceID}, "node": {strconv.Itoa(int(query.Node))}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return ApprovalTask{}, ErrDependencyUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Integration-Timestamp", r.now().UTC().Format(time.RFC3339Nano))
	if requestID := strings.TrimSpace(requestctx.ID(ctx)); requestID != "" {
		if !validApprovalTaskRequestID(requestID) {
			return ApprovalTask{}, ErrDependencyUnavailable
		}
		request.Header.Set("X-Request-ID", requestID)
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(r.nonceReader, nonce); err != nil {
		return ApprovalTask{}, ErrDependencyUnavailable
	}
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	response, err := r.client.Do(request)
	if err != nil {
		return ApprovalTask{}, ErrDependencyUnavailable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxApprovalTaskResponseBytes+1))
	if err != nil || len(raw) > maxApprovalTaskResponseBytes || response.StatusCode != http.StatusOK || !approvalTaskJSONContentType(response.Header.Get("Content-Type")) {
		return ApprovalTask{}, ErrDependencyUnavailable
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Data      struct {
			EngineTaskID     string `json:"engine_task_id"`
			EngineInstanceID string `json:"engine_instance_id"`
			Node             uint8  `json:"node"`
			ApproverID       string `json:"approver_id"`
			Status           string `json:"status"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Code != "OK" || !validApprovalTaskRequestID(envelope.RequestID) ||
		!validApprovalTaskIdentity(envelope.Data.EngineTaskID) || envelope.Data.EngineInstanceID != query.EngineInstanceID || envelope.Data.Node != query.Node ||
		envelope.Data.ApproverID != query.ApproverID || envelope.Data.Status != "PENDING" {
		return ApprovalTask{}, ErrDependencyUnavailable
	}
	return ApprovalTask{EngineTaskID: envelope.Data.EngineTaskID, EngineInstanceID: envelope.Data.EngineInstanceID, Node: envelope.Data.Node, ApproverID: envelope.Data.ApproverID}, nil
}

func validApprovalTaskIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxApprovalTaskIdentityLength && !strings.ContainsAny(value, "\r\n\x00")
}

func validApprovalTaskRequestID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 && !strings.ContainsAny(value, "\r\n")
}

func validApprovalTaskURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func rejectApprovalTaskRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func approvalTaskJSONContentType(value string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return strings.EqualFold(mediaType, "application/json")
}
