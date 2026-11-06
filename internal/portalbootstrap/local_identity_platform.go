package portalbootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type LocalIdentityCredential struct{ ClientID, ClientSecret string }
type LocalIdentityPlatformOptions struct {
	PlatformBaseURL, TokenURL, TenantID                             string
	Provision, RoleAssign, RoleRevoke, BindingWrite, BindingDisable LocalIdentityCredential
	TLS                                                             integrationhttp.TLSOptions
	HTTPClient                                                      *http.Client
}
type localIdentityPlatform struct {
	base, tenant string
	clients      map[string]*http.Client
}
type localPlatformReceipt struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      struct {
		PlatformUserID  string `json:"platform_user_id"`
		AccountNo       string `json:"account_no,omitempty"`
		ApplicationCode string `json:"application_code,omitempty"`
		RoleCode        string `json:"role_code,omitempty"`
		Status          string `json:"status,omitempty"`
	} `json:"data"`
}

// NewLocalIdentityPlatform 构造独立凭据的真实平台身份适配器。
// 参数：ctx 为生命周期，options 为固定租户、地址及五种精确职责凭据。
// 返回值：真实HTTP适配器；缺配置、凭据复用或非法端点时失败。
func NewLocalIdentityPlatform(ctx context.Context, options LocalIdentityPlatformOptions) (localidentity.Platform, error) {
	if ctx == nil || strings.TrimSpace(options.TenantID) == "" {
		return nil, localidentity.ErrUnavailable
	}
	for _, raw := range []string{options.PlatformBaseURL, options.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || raw != strings.TrimSpace(raw) {
			return nil, localidentity.ErrUnavailable
		}
	}
	if err := options.TLS.ValidateEndpoints(options.PlatformBaseURL, options.TokenURL); err != nil {
		return nil, err
	}
	transport, err := integrationhttp.NewTransport(options.TLS, 3*time.Second)
	if err != nil {
		return nil, err
	}
	var base http.RoundTripper = transport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		base = options.HTTPClient.Transport
	}
	credentials := map[string]LocalIdentityCredential{"external_user.provision": options.Provision, "application_role.assign": options.RoleAssign, "application_role.revoke": options.RoleRevoke, "portal.identity_mapping.provision": options.BindingWrite, "portal.identity_mapping.disable": options.BindingDisable}
	clients := make(map[string]*http.Client)
	seen := make(map[string]bool)
	for scope, credential := range credentials {
		if credential.ClientID == "" || credential.ClientSecret == "" || credential.ClientID != strings.TrimSpace(credential.ClientID) || credential.ClientSecret != strings.TrimSpace(credential.ClientSecret) || seen[credential.ClientID] {
			return nil, localidentity.ErrUnavailable
		}
		seen[credential.ClientID] = true
		tokenHTTP := &http.Client{Transport: base, Timeout: 5 * time.Second, CheckRedirect: localPlatformRedirect}
		tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, tokenHTTP)
		oauth := clientcredentials.Config{ClientID: credential.ClientID, ClientSecret: credential.ClientSecret, TokenURL: options.TokenURL, Scopes: []string{scope}, AuthStyle: oauth2.AuthStyleInHeader}
		clients[scope] = &http.Client{Transport: &oauth2.Transport{Source: oauth.TokenSource(tokenCtx), Base: base}, Timeout: 10 * time.Second, CheckRedirect: localPlatformRedirect}
	}
	return &localIdentityPlatform{base: strings.TrimRight(options.PlatformBaseURL, "/"), tenant: options.TenantID, clients: clients}, nil
}

// localPlatformRedirect 禁止管理请求或OAuth凭据随重定向外发。
// 参数：请求和已跟随链仅由HTTP客户端提供。
// 返回值：停止重定向的标准错误。
func localPlatformRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// localOperationKey 隔离本地门户与CRM幂等坐标。
// 参数：tenant/op/stage 为固定租户、持久操作号和真实调用阶段。
// 返回值：有界不可碰撞的请求键。
func localOperationKey(tenant, op, stage string) string {
	sum := sha256.Sum256([]byte("portal-local-v1\x00" + tenant + "\x00" + op + "\x00" + stage))
	return "portal-local-v1:" + hex.EncodeToString(sum[:])
}

// call 发出精确职责的防重放请求并拒绝未知或损坏回执。
// 参数：ctx 为当前请求，scope/method/path 为固定协议，op 为原操作号，payload 为受控请求。
// 返回值：完整真实回执；网络、协议或解析失败时不得推进操作。
func (p *localIdentityPlatform) call(ctx context.Context, scope, method, path, op string, payload any) (localPlatformReceipt, error) {
	var receipt localPlatformReceipt
	if p == nil || p.clients[scope] == nil || op == "" || len(op) > 64 {
		return receipt, localidentity.ErrUnavailable
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return receipt, err
	}
	request, err := http.NewRequestWithContext(ctx, method, p.base+path, bytes.NewReader(raw))
	if err != nil {
		return receipt, err
	}
	nonce := make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return receipt, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", localOperationKey(p.tenant, op, scope))
	request.Header.Set("X-Integration-Timestamp", time.Now().UTC().Format(time.RFC3339Nano))
	request.Header.Set("X-Integration-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	request.Header.Set("X-Request-ID", requestctx.ID(ctx))
	response, err := p.clients[scope].Do(request)
	if err != nil {
		return receipt, localidentity.ErrUnavailable
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || response.StatusCode != http.StatusOK {
		return receipt, localidentity.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF || receipt.Code != "OK" || strings.TrimSpace(receipt.RequestID) == "" || len(receipt.RequestID) > 128 || strings.ContainsAny(receipt.RequestID, "\x00\r\n") {
		return receipt, localidentity.ErrUnavailable
	}
	return receipt, nil
}

// Provision 创建或恢复真实平台外部身份，不构造本地假账号。
// 参数：ctx/op 为原管理操作，contact 为冻结登记信息。
// 返回值：平台确认的主体及账号；损坏回执失败。
func (p *localIdentityPlatform) Provision(ctx context.Context, op string, contact localidentity.ContactInput) (localidentity.PlatformIdentity, error) {
	if strings.TrimSpace(contact.DisplayName) == "" || strings.TrimSpace(contact.Phone) == "" {
		return localidentity.PlatformIdentity{}, localidentity.ErrInvalid
	}
	receipt, err := p.call(ctx, "external_user.provision", http.MethodPost, "/api/v1/internal/external-users", op, map[string]string{"display_name": contact.DisplayName, "mobile": contact.Phone})
	if err != nil {
		return localidentity.PlatformIdentity{}, err
	}
	id, account := receipt.Data.PlatformUserID, receipt.Data.AccountNo
	if !localPlatformSubject(id) || account == "" || len(account) > 32 || account != strings.TrimSpace(account) || strings.ContainsAny(account, "\x00\r\n") {
		return localidentity.PlatformIdentity{}, localidentity.ErrUnavailable
	}
	return localidentity.PlatformIdentity{PlatformUserID: id, AccountNo: account}, nil
}

// localPlatformSubject 限制平台主体可无歧义编码为本地版本化客户引用。
// 参数：subject 为平台用户坐标。
// 返回值：是否可安全用于精确URL及身份匹配。
func localPlatformSubject(subject string) bool {
	_, err := localidentity.CustomerBindingRef(1, subject)
	return err == nil
}

// role 仅修改portal_customer精确角色，不授权管理角色。
// 参数：ctx/subject/op 为真实操作，revoke 表示撤销。
// 返回值：严格匹配平台角色回执或错误。
func (p *localIdentityPlatform) role(ctx context.Context, subject, op string, revoke bool) error {
	if !localPlatformSubject(subject) {
		return localidentity.ErrInvalid
	}
	scope, path, status := "application_role.assign", "/api/v1/internal/application-roles", "ACTIVE"
	if revoke {
		scope, path, status = "application_role.revoke", path+"/revoke", "DISABLED"
	}
	receipt, err := p.call(ctx, scope, http.MethodPost, path, op, map[string]string{"platform_user_id": subject, "application_code": "customer_portal", "role_code": "portal_customer"})
	if err != nil {
		return err
	}
	if receipt.Data.PlatformUserID != subject || receipt.Data.ApplicationCode != "customer_portal" || receipt.Data.RoleCode != "portal_customer" || receipt.Data.Status != status {
		return localidentity.ErrUnavailable
	}
	return nil
}

// AssignRole 依据原操作授予门户客户角色。
// 参数：ctx、subject及op为当前请求、真实身份及持久操作号。
// 返回值：真实角色回执校验结果。
func (p *localIdentityPlatform) AssignRole(ctx context.Context, subject, op string) error {
	return p.role(ctx, subject, op, false)
}

// RevokeRole 依据原操作撤销门户客户角色。
// 参数：ctx、subject及op为当前请求、真实身份及持久操作号。
// 返回值：真实撤销回执校验结果。
func (p *localIdentityPlatform) RevokeRole(ctx context.Context, subject, op string) error {
	return p.role(ctx, subject, op, true)
}

// binding 写入或禁用当前应用的真实平台客户引用。
// 参数：ctx/subject/customer/op 为精确业务坐标，disable 表示停用。
// 返回值：平台绑定回执校验结果。
func (p *localIdentityPlatform) binding(ctx context.Context, subject string, customer uint64, op string, disable bool) error {
	reference, err := localidentity.CustomerBindingRef(customer, subject)
	if err != nil {
		return err
	}
	scope, method, status := "portal.identity_mapping.provision", http.MethodPut, "ACTIVE"
	path := "/api/v1/internal/external-users/" + url.PathEscape(subject) + "/customer-binding"
	if disable {
		scope, method, status = "portal.identity_mapping.disable", http.MethodPost, "DISABLED"
		path += "/disable"
	}
	receipt, err := p.call(ctx, scope, method, path, op, map[string]string{"customer_ref": reference})
	if err != nil {
		return err
	}
	if receipt.Data.PlatformUserID != subject || receipt.Data.ApplicationCode != "customer_portal" || receipt.Data.Status != status {
		return localidentity.ErrUnavailable
	}
	return nil
}

// BindCustomer 绑定本地权威客户，不把数字当作CRM客户。
// 参数：ctx/subject/customer/op 为真实持久操作坐标。
// 返回值：平台真实绑定结果。
func (p *localIdentityPlatform) BindCustomer(ctx context.Context, subject string, customer uint64, op string) error {
	return p.binding(ctx, subject, customer, op, false)
}

// DisableBinding 停用真实客户引用且保留本地失败操作供精确续跑。
// 参数：ctx/subject/customer/op 为原操作坐标。
// 返回值：平台真实禁用结果。
func (p *localIdentityPlatform) DisableBinding(ctx context.Context, subject string, customer uint64, op string) error {
	return p.binding(ctx, subject, customer, op, true)
}
