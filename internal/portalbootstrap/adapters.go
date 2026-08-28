package portalbootstrap

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const maxAdapterResponseBytes = 64 << 10

// OIDC 适配器遵循平台发布的 Discovery/JWKS 契约；只有签名、issuer、audience、过期时间、
// nonce 以及平台扩展声明全部通过后，才向账号服务返回 Claims。
type OIDCAdapter struct {
	config          oauth2.Config
	verifier        *oidc.IDTokenVerifier
	provider        *oidc.Provider
	httpClient      *http.Client
	platformBaseURL string
	roleConfigHash  string
	idpHint         string
	expectedContext sharedauthorization.Expectation
}

func NewOIDCAdapter(ctx context.Context, config Config) (*OIDCAdapter, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if config.OIDCBackchannelURL != "" {
		// 容器内可把公开 issuer 的网络请求改写到后通道地址，但请求语义和令牌 issuer 仍保持
		// 公开地址，避免内部 DNS 结构泄露到浏览器协议。
		publicURL, err := url.Parse(strings.TrimRight(config.OIDCIssuer, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse OIDC issuer: %w", err)
		}
		backchannelURL, err := url.Parse(strings.TrimRight(config.OIDCBackchannelURL, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse OIDC backchannel: %w", err)
		}
		httpClient.Transport = &issuerTransport{base: http.DefaultTransport, public: publicURL, backchannel: backchannelURL}
	}
	oidcContext := oidc.ClientContext(ctx, httpClient)
	provider, err := oidc.NewProvider(oidcContext, strings.TrimRight(config.OIDCIssuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("load OIDC discovery: %w", err)
	}
	return &OIDCAdapter{
		config:   oauth2.Config{ClientID: config.OIDCClientID, ClientSecret: config.OIDCClientSecret, Endpoint: provider.Endpoint(), RedirectURL: config.OIDCRedirectURI, Scopes: config.OIDCScopes},
		verifier: provider.Verifier(&oidc.Config{ClientID: config.OIDCClientID}), provider: provider, httpClient: httpClient,
		platformBaseURL: strings.TrimRight(config.PlatformBaseURL, "/"), roleConfigHash: config.RoleConfigHash,
		idpHint:         config.OIDCIdentityProviderHint,
		expectedContext: sharedauthorization.Expectation{ClientID: config.OIDCClientID, ApplicationCode: config.PlatformApplicationCode, EnvironmentCode: config.PlatformEnvironmentCode, RoleConfigHash: config.RoleConfigHash},
	}, nil
}

func (a *OIDCAdapter) AuthorizationURL(state, nonce, codeChallenge, _ string) (string, error) {
	if state == "" || nonce == "" || codeChallenge == "" {
		return "", errors.New("OIDC login parameters are incomplete")
	}
	options := []oauth2.AuthCodeOption{
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	if a.idpHint != "" {
		options = append(options, oauth2.SetAuthURLParam("kc_idp_hint", a.idpHint))
	}
	return a.config.AuthCodeURL(state, options...), nil
}

func (a *OIDCAdapter) ExchangeAndValidate(ctx context.Context, code, verifier, nonce string) (account.Claims, error) {
	// 授权码必须与一次性 PKCE verifier 和 nonce 同时消费；缺少任一绑定都拒绝交换。
	if strings.TrimSpace(code) == "" || verifier == "" || nonce == "" {
		return account.Claims{}, errors.New("OIDC callback parameters are incomplete")
	}
	oidcContext := oidc.ClientContext(ctx, a.httpClient)
	token, err := a.config.Exchange(oidcContext, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return account.Claims{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return account.Claims{}, errors.New("OIDC response has no ID token")
	}
	idToken, err := a.verifier.Verify(oidcContext, rawIDToken)
	if err != nil {
		return account.Claims{}, fmt.Errorf("verify ID token: %w", err)
	}
	var raw compactOIDCClaims
	if err := idToken.Claims(&raw); err != nil {
		return account.Claims{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	// 紧凑 Keycloak ID Token 只保证稳定身份；tenant_id、角色和权限由基础平台
	// authorization-context 按当前 access token 返回。
	if !validCompactPortalIdentity(raw, nonce, token.AccessToken) {
		return account.Claims{}, errors.New("OIDC identity claims are invalid")
	}
	claims := compactPortalClaims(raw, a.roleConfigHash, earliestExpiry(idToken.Expiry, token.Expiry), token.AccessToken)
	// 详细角色、权限和授权版本由基础平台按当前访问令牌计算，不能依赖 Keycloak
	// Token 中可能过期的业务权限快照。
	contextClaims, effectiveToken, contextErr := a.authorizationContextWithRefresh(ctx, token)
	if contextErr == nil {
		if contextClaims.Subject != claims.Subject || contextClaims.IdentityID != claims.IdentityID || (claims.TenantID != "" && contextClaims.TenantID != claims.TenantID) {
			return account.Claims{}, errors.New("OIDC authorization context identity does not match token")
		}
		contextClaims.RoleConfigHash = a.roleConfigHash
		contextClaims.OIDCSessionID = claims.OIDCSessionID
		contextClaims.ExpiresAt = earliestExpiry(claims.ExpiresAt, effectiveToken.Expiry)
		contextClaims.AccessToken = effectiveToken.AccessToken
		return contextClaims, nil
	}
	return account.Claims{}, fmt.Errorf("load portal authorization context: %w", contextErr)
}

func compactPortalClaims(raw compactOIDCClaims, roleConfigHash string, expiresAt time.Time, accessToken string) account.Claims {
	return account.Claims{
		Subject:        raw.Subject,
		OIDCSessionID:  strings.TrimSpace(raw.SessionID),
		IdentityID:     raw.IdentityID,
		TenantID:       raw.TenantID,
		RoleConfigHash: roleConfigHash,
		ExpiresAt:      expiresAt,
		AccessToken:    accessToken,
	}
}

func (a *OIDCAdapter) UserInfo(ctx context.Context, accessToken string) (account.Claims, error) {
	if strings.TrimSpace(accessToken) == "" {
		return account.Claims{}, errors.New("OIDC access token is missing")
	}
	contextClaims, err := a.authorizationContext(ctx, accessToken)
	if err != nil {
		return account.Claims{}, fmt.Errorf("load portal authorization context: %w", err)
	}
	contextClaims.RoleConfigHash = a.roleConfigHash
	return contextClaims, nil
}

type compactOIDCClaims struct {
	Subject    string `json:"sub"`
	IdentityID string `json:"identity_id"`
	Nonce      string `json:"nonce"`
	TenantID   string `json:"tenant_id"`
	TokenUse   string `json:"token_use"`
	SessionID  string `json:"sid"`
}

func validCompactPortalIdentity(raw compactOIDCClaims, nonce, accessToken string) bool {
	return raw.Nonce == nonce && raw.TokenUse == "id_token" && strings.TrimSpace(raw.Subject) != "" &&
		raw.Subject == strings.TrimSpace(raw.Subject) && strings.TrimSpace(raw.IdentityID) != "" && raw.IdentityID == strings.TrimSpace(raw.IdentityID) &&
		strings.TrimSpace(accessToken) != ""
}

func (a *OIDCAdapter) authorizationContext(ctx context.Context, accessToken string) (account.Claims, error) {
	if a.platformBaseURL == "" {
		return account.Claims{}, errors.New("portal platform authorization context endpoint is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.platformBaseURL+"/oauth2/authorization-context", nil)
	if err != nil {
		return account.Claims{}, fmt.Errorf("create portal authorization context request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return account.Claims{}, fmt.Errorf("request portal authorization context: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return account.Claims{}, fmt.Errorf("portal authorization context returned HTTP 401: %w", sharedauthorization.ErrTokenRejected)
		case http.StatusForbidden:
			return account.Claims{}, fmt.Errorf("portal authorization context returned HTTP 403: %w", sharedauthorization.ErrForbidden)
		default:
			if resp.StatusCode >= http.StatusInternalServerError {
				return account.Claims{}, fmt.Errorf("portal authorization context returned HTTP %d: %w", resp.StatusCode, sharedauthorization.ErrUnavailable)
			}
			return account.Claims{}, fmt.Errorf("portal authorization context returned unexpected HTTP %d", resp.StatusCode)
		}
	}
	var raw sharedauthorization.Response
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return account.Claims{}, fmt.Errorf("decode portal authorization context: %w", err)
	}
	scopes, _, err := sharedauthorization.Validate(raw, a.expectedContext)
	if err != nil {
		return account.Claims{}, fmt.Errorf("validate portal authorization context: %w", err)
	}
	return account.Claims{Subject: raw.Subject, IdentityID: raw.IdentityID, PersonID: raw.PersonID, TenantID: raw.TenantID, Roles: raw.Roles, Permissions: raw.Permissions, DataScopes: scopes, AuthzRevision: raw.AuthorizationRevision, CustomerRef: raw.CustomerRef, LoginIP: loginip.Normalize(raw.UserLoginIP)}, nil
}

func (a *OIDCAdapter) authorizationContextWithRefresh(ctx context.Context, token *oauth2.Token) (account.Claims, *oauth2.Token, error) {
	claims, err := a.authorizationContext(ctx, token.AccessToken)
	if err == nil || !errors.Is(err, sharedauthorization.ErrTokenRejected) || strings.TrimSpace(token.RefreshToken) == "" {
		return claims, token, err
	}
	// A platform 401 can indicate an access token that expired between the
	// authorization-code exchange and the online authorization request. Force
	// one OAuth refresh and one retry; never loop and never retry 403/5xx.
	stale := *token
	stale.Expiry = time.Now().Add(-time.Minute)
	refreshed, refreshErr := a.config.TokenSource(oidc.ClientContext(ctx, a.httpClient), &stale).Token()
	if refreshErr != nil {
		return account.Claims{}, token, fmt.Errorf("refresh portal access token after authorization 401: %w", refreshErr)
	}
	claims, err = a.authorizationContext(ctx, refreshed.AccessToken)
	return claims, refreshed, err
}

type issuerTransport struct {
	base                http.RoundTripper
	public, backchannel *url.URL
}

func (t *issuerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// 只改写与公开 issuer 精确同源的请求；其他 OAuth 或业务地址沿用原 Transport，防止凭据
	// 被后通道配置吸收到非预期主机。
	if request.URL.Scheme != t.public.Scheme || request.URL.Host != t.public.Host {
		return t.base.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host = t.backchannel.Scheme, t.backchannel.Host
	return t.base.RoundTrip(clone)
}

type SecretProtector struct{ codec *AEADCodec }

func NewSecretProtector(key []byte) (*SecretProtector, error) {
	codec, err := NewAEADCodec(key)
	if err != nil {
		return nil, err
	}
	return &SecretProtector{codec: codec}, nil
}
func (p *SecretProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	return p.codec.Encrypt(value)
}
func (p *SecretProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	return p.codec.Decrypt(value)
}

type CRMInviteClientOptions struct {
	BaseURL, TokenURL, ClientID, ClientSecret, Scope string
	HTTPClient                                       *http.Client
	Now                                              func() time.Time
	NonceReader                                      io.Reader
}

type CRMInviteClient struct {
	baseURL     string
	client      *http.Client
	now         func() time.Time
	nonceReader io.Reader
}

// 构造 Portal 专用的 client-credentials 调用方。取令牌时不发送 audience：平台使用部署级应用
// JWT audience 签名，CRM 再依据自身配置独立验签并校验精确客户端 subject 与 scope。
func NewCRMInviteClient(ctx context.Context, options CRMInviteClientOptions) (*CRMInviteClient, error) {
	for name, value := range map[string]string{"base URL": options.BaseURL, "token URL": options.TokenURL, "client ID": options.ClientID, "client secret": options.ClientSecret} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("CRM invitation %s is required", name)
		}
	}
	if strings.TrimSpace(options.Scope) != "portal.invite.verify" {
		return nil, errors.New("CRM invitation machine scope is invalid")
	}
	baseTransport := http.DefaultTransport
	if options.HTTPClient != nil && options.HTTPClient.Transport != nil {
		baseTransport = options.HTTPClient.Transport
	}
	tokenHTTPClient := &http.Client{Transport: baseTransport, Timeout: 5 * time.Second}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, tokenHTTPClient)
	credentials := clientcredentials.Config{
		ClientID: options.ClientID, ClientSecret: options.ClientSecret,
		TokenURL: options.TokenURL, Scopes: []string{options.Scope},
		AuthStyle: oauth2.AuthStyleInHeader,
	}
	client := &http.Client{Transport: &oauth2.Transport{Source: credentials.TokenSource(tokenContext), Base: baseTransport}, Timeout: 10 * time.Second}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	nonceReader := options.NonceReader
	if nonceReader == nil {
		nonceReader = rand.Reader
	}
	return &CRMInviteClient{baseURL: strings.TrimRight(options.BaseURL, "/"), client: client, now: now, nonceReader: nonceReader}, nil
}
func (c *CRMInviteClient) Verify(ctx context.Context, token string) (account.VerifiedInvite, error) {
	var result struct {
		TenantID       string  `json:"tenant_id"`
		PlatformUserID string  `json:"platform_user_id"`
		CustomerID     uint64  `json:"customer_id"`
		ContactID      *uint64 `json:"contact_id"`
	}
	if err := c.post(ctx, "/internal/portal/invites/verify", map[string]string{"token": token}, &result); err != nil {
		return account.VerifiedInvite{}, err
	}
	if strings.TrimSpace(result.TenantID) == "" || strings.TrimSpace(result.PlatformUserID) == "" || result.CustomerID == 0 {
		return account.VerifiedInvite{}, errors.New("CRM invitation endpoint returned an invalid response")
	}
	return account.VerifiedInvite{TenantID: result.TenantID, ExpectedPlatformUserID: result.PlatformUserID, CustomerID: result.CustomerID, ContactID: result.ContactID}, nil
}
func (c *CRMInviteClient) Consume(ctx context.Context, token, subject string) error {
	requestID := requestctx.ID(ctx)
	if requestID == "" {
		requestID = requestctx.NewID()
	}
	return c.post(ctx, "/internal/portal/invites/consume", map[string]string{"token": token, "platform_user_id": subject, "request_id": requestID}, nil)
}
func (c *CRMInviteClient) post(ctx context.Context, path string, body any, target any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errors.New("encode CRM invitation request failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("create CRM invitation request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Integration-Timestamp", c.now().UTC().Format(time.RFC3339Nano))
	nonce, err := c.newNonce()
	if err != nil {
		return err
	}
	request.Header.Set("X-Integration-Nonce", nonce)
	response, err := c.client.Do(request)
	if err != nil {
		return safeCRMInviteTransportError(err)
	}
	defer response.Body.Close()
	rawResponse, err := io.ReadAll(io.LimitReader(response.Body, maxAdapterResponseBytes+1))
	if err != nil || len(rawResponse) > maxAdapterResponseBytes {
		return errors.New("CRM invitation endpoint returned an invalid response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("CRM invitation endpoint returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code string          `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rawResponse, &envelope); err != nil || envelope.Code != "OK" {
		return errors.New("CRM invitation endpoint returned an invalid response")
	}
	if target != nil && (len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) || json.Unmarshal(envelope.Data, target) != nil) {
		return errors.New("CRM invitation endpoint returned an invalid response")
	}
	return nil
}

func (c *CRMInviteClient) newNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(c.nonceReader, raw); err != nil {
		return "", errors.New("generate CRM invitation request nonce failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func safeCRMInviteTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("CRM invitation request timed out")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("CRM invitation request timed out")
	}
	return errors.New("CRM invitation transport failed")
}

func earliestExpiry(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if !value.IsZero() && (result.IsZero() || value.Before(result)) {
			result = value.UTC()
		}
	}
	return result
}
func hashText(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
