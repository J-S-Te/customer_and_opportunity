package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"golang.org/x/oauth2"
)

type OIDCOptions struct {
	Issuer, BackchannelBaseURL, PlatformBaseURL, ClientID, ClientSecret, RedirectURI string
	ApplicationCode, EnvironmentCode                                                 string
	IdentityProviderHint                                                             string
	Scopes                                                                           []string
}

type verifiedClaims struct {
	Subject, IdentityID, TenantID, PersonID, DisplayName, RoleConfigHash string
	PrimaryOrgID                                                         string
	OrganizationIDs                                                      []string
	DataScopes                                                           []sharedauth.DataScope
	Roles, Permissions                                                   []string
	AuthzRevision                                                        uint64
	ExpiresAt                                                            time.Time
	AccessToken                                                          string
}

type oidcClient interface {
	AuthorizationURL(state, nonce, verifier string) string
	Exchange(context.Context, string, string, string) (verifiedClaims, error)
	UserInfo(context.Context, string) (verifiedClaims, error)
}

type forcedLoginOIDCClient interface {
	AuthorizationURLWithPrompt(state, nonce, verifier string, forceLogin bool) string
}

type platformClaims struct {
	Subject           string `json:"sub"`
	IdentityID        string `json:"identity_id"`
	Nonce             string `json:"nonce"`
	TenantID          string `json:"tenant_id"`
	PersonID          string `json:"person_id"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	TokenUse          string `json:"token_use"`
}

type platformOIDCClient struct {
	config               oauth2.Config
	verifier             *oidc.IDTokenVerifier
	provider             *oidc.Provider
	httpClient           *http.Client
	platformBaseURL      string
	identityProviderHint string
	endSessionEndpoint   string
	expectedContext      sharedauthorization.Expectation
}

func NewPlatformOIDCClient(ctx context.Context, options OIDCOptions) (*platformOIDCClient, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if options.BackchannelBaseURL != "" {
		// 浏览器仍使用公开 issuer；仅把服务端发现、换码和 UserInfo 请求改写到容器内地址，
		// 从而保持令牌 iss 校验不变，同时避免容器回环访问公网入口。
		publicURL, err := url.Parse(strings.TrimRight(options.Issuer, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse CRM OIDC issuer: %w", err)
		}
		backchannelURL, err := url.Parse(strings.TrimRight(options.BackchannelBaseURL, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse CRM OIDC backchannel: %w", err)
		}
		httpClient.Transport = &backchannelTransport{base: http.DefaultTransport, public: publicURL, backchannel: backchannelURL}
	}
	oidcContext := oidc.ClientContext(ctx, httpClient)
	provider, err := oidc.NewProvider(oidcContext, strings.TrimRight(options.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("load CRM OIDC discovery: %w", err)
	}
	endSessionEndpoint, err := discoverEndSessionEndpoint(provider, options.Issuer)
	if err != nil {
		return nil, err
	}
	return &platformOIDCClient{
		httpClient: httpClient, provider: provider,
		platformBaseURL:      strings.TrimRight(options.PlatformBaseURL, "/"),
		identityProviderHint: strings.TrimSpace(options.IdentityProviderHint),
		endSessionEndpoint:   endSessionEndpoint,
		expectedContext:      sharedauthorization.Expectation{ClientID: options.ClientID, ApplicationCode: options.ApplicationCode, EnvironmentCode: options.EnvironmentCode},
		verifier:             provider.Verifier(&oidc.Config{ClientID: options.ClientID}),
		config:               oauth2.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: options.RedirectURI, Scopes: options.Scopes},
	}, nil
}

// EndSessionEndpoint returns the provider-owned RP-initiated logout endpoint.
// The value comes from OIDC Discovery; the legacy fallback is resolved once at
// startup so request handlers never infer provider-specific paths themselves.
func (c *platformOIDCClient) EndSessionEndpoint() string {
	if c == nil {
		return ""
	}
	return c.endSessionEndpoint
}

func discoverEndSessionEndpoint(provider *oidc.Provider, issuer string) (string, error) {
	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return "", fmt.Errorf("decode CRM OIDC discovery metadata: %w", err)
	}
	endpoint := strings.TrimSpace(metadata.EndSessionEndpoint)
	if endpoint == "" {
		// Compatibility for the original base-platform issuer, whose historical
		// discovery documents did not always publish end_session_endpoint.
		endpoint = strings.TrimRight(strings.TrimSpace(issuer), "/") + "/oauth2/logout"
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("CRM OIDC end_session_endpoint must be a valid HTTP(S) URL")
	}
	return parsed.String(), nil
}

func (c *platformOIDCClient) AuthorizationURL(state, nonce, verifier string) string {
	return c.AuthorizationURLWithPrompt(state, nonce, verifier, false)
}

func (c *platformOIDCClient) AuthorizationURLWithPrompt(state, nonce, verifier string, forceLogin bool) string {
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if forceLogin {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"))
	}
	if c.identityProviderHint != "" {
		// Keycloak 的 kc_idp_hint 会直接把认证请求交给基础平台 Broker。
		// 门户已有基础平台会话时，整个往返无需展示 Keycloak 登录或账号补充页面。
		options = append(options, oauth2.SetAuthURLParam("kc_idp_hint", c.identityProviderHint))
	}
	return c.config.AuthCodeURL(state, options...)
}

func (c *platformOIDCClient) Exchange(ctx context.Context, code, verifier, nonce string) (verifiedClaims, error) {
	if strings.TrimSpace(code) == "" || verifier == "" || nonce == "" {
		return verifiedClaims{}, errors.New("CRM OIDC callback parameters are incomplete")
	}
	oidcContext := oidc.ClientContext(ctx, c.httpClient)
	token, err := c.config.Exchange(oidcContext, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("exchange CRM OIDC authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return verifiedClaims{}, errors.New("CRM OIDC response has no ID token")
	}
	idToken, err := c.verifier.Verify(oidcContext, rawIDToken)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("verify CRM OIDC ID token: %w", err)
	}
	var raw platformClaims
	if err := idToken.Claims(&raw); err != nil {
		return verifiedClaims{}, fmt.Errorf("decode CRM OIDC ID token: %w", err)
	}
	if raw.Nonce != nonce || raw.TokenUse != "id_token" || token.AccessToken == "" {
		return verifiedClaims{}, errors.New("CRM OIDC ID token purpose, nonce or access token is invalid")
	}
	// ID token 的 sub 是平台规范中的 canonical identity；若令牌同时返回别名，
	// 两者必须一致，避免 CRM 在不同令牌载荷间产生两个主体。
	claims := claimsFromPlatform(raw)
	claims.AccessToken = token.AccessToken
	claims.ExpiresAt = earliestExpiry(idToken.Expiry, token.Expiry)
	// Keycloak Token 只承载稳定身份和少量 OIDC 元数据。详细角色、权限和组织范围
	// 由基础平台授权上下文接口按当前快照返回，避免权限变更等待 Token 过期。
	contextClaims, effectiveToken, err := c.authorizationContextWithRefresh(ctx, token)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("load CRM authorization context: %w", err)
	}
	if contextClaims.Subject != claims.Subject || contextClaims.IdentityID != claims.IdentityID || (claims.TenantID != "" && contextClaims.TenantID != claims.TenantID) {
		return verifiedClaims{}, errors.New("CRM authorization context identity does not match OIDC identity")
	}
	contextClaims.DisplayName = claims.DisplayName
	contextClaims.PersonID = firstNonEmpty(contextClaims.PersonID, claims.PersonID)
	contextClaims.ExpiresAt = earliestExpiry(claims.ExpiresAt, effectiveToken.Expiry)
	contextClaims.AccessToken = effectiveToken.AccessToken
	return contextClaims, nil
}

func (c *platformOIDCClient) UserInfo(ctx context.Context, accessToken string) (verifiedClaims, error) {
	if strings.TrimSpace(accessToken) == "" {
		return verifiedClaims{}, errors.New("CRM OIDC access token is missing")
	}
	claims, err := c.AuthorizationContext(ctx, accessToken)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("load CRM authorization context: %w", err)
	}
	oidcContext := oidc.ClientContext(ctx, c.httpClient)
	info, userInfoErr := c.provider.UserInfo(oidcContext, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken, TokenType: "Bearer"}))
	if userInfoErr != nil {
		return verifiedClaims{}, fmt.Errorf("load CRM OIDC UserInfo: %w", userInfoErr)
	}
	var raw struct {
		Subject           string `json:"sub"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	if userInfoErr = info.Claims(&raw); userInfoErr != nil || raw.Subject == "" {
		return verifiedClaims{}, fmt.Errorf("decode CRM OIDC UserInfo: %w", userInfoErr)
	}
	if claims.Subject != "" && raw.Subject != claims.Subject {
		return verifiedClaims{}, errors.New("CRM UserInfo subject changed")
	}
	claims.DisplayName = firstNonEmpty(raw.Name, raw.PreferredUsername, raw.Email)
	return claims, nil
}

func (c *platformOIDCClient) AuthorizationContext(ctx context.Context, accessToken string) (verifiedClaims, error) {
	if c.platformBaseURL == "" {
		return verifiedClaims{}, errors.New("CRM platform authorization context endpoint is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.platformBaseURL+"/oauth2/authorization-context", nil)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("create CRM authorization context request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("request CRM authorization context: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return verifiedClaims{}, fmt.Errorf("CRM authorization context returned HTTP 401: %w", sharedauthorization.ErrTokenRejected)
		case http.StatusForbidden:
			return verifiedClaims{}, fmt.Errorf("CRM authorization context returned HTTP 403: %w", sharedauthorization.ErrForbidden)
		default:
			if resp.StatusCode >= http.StatusInternalServerError {
				return verifiedClaims{}, fmt.Errorf("CRM authorization context returned HTTP %d: %w", resp.StatusCode, sharedauthorization.ErrUnavailable)
			}
			return verifiedClaims{}, fmt.Errorf("CRM authorization context returned unexpected HTTP %d", resp.StatusCode)
		}
	}
	var raw sharedauthorization.Response
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return verifiedClaims{}, fmt.Errorf("decode CRM authorization context: %w", err)
	}
	scopes, decision, err := sharedauthorization.Validate(raw, c.expectedContext)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("validate CRM authorization context: %w", err)
	}
	primaryOrgID := ""
	if len(decision.OrganizationIDs) > 0 {
		primaryOrgID = decision.OrganizationIDs[0]
	}
	return verifiedClaims{Subject: raw.Subject, IdentityID: raw.IdentityID, TenantID: raw.TenantID, PersonID: raw.PersonID, PrimaryOrgID: primaryOrgID, Roles: raw.Roles, Permissions: raw.Permissions, OrganizationIDs: decision.OrganizationIDs, DataScopes: scopes, AuthzRevision: raw.AuthorizationRevision}, nil
}

func (c *platformOIDCClient) authorizationContextWithRefresh(ctx context.Context, token *oauth2.Token) (verifiedClaims, *oauth2.Token, error) {
	claims, err := c.AuthorizationContext(ctx, token.AccessToken)
	if err == nil || !errors.Is(err, sharedauthorization.ErrTokenRejected) || strings.TrimSpace(token.RefreshToken) == "" {
		return claims, token, err
	}
	stale := *token
	stale.Expiry = time.Now().Add(-time.Minute)
	refreshed, refreshErr := c.config.TokenSource(oidc.ClientContext(ctx, c.httpClient), &stale).Token()
	if refreshErr != nil {
		return verifiedClaims{}, token, fmt.Errorf("refresh CRM access token after authorization 401: %w", refreshErr)
	}
	claims, err = c.AuthorizationContext(ctx, refreshed.AccessToken)
	return claims, refreshed, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func claimsFromPlatform(raw platformClaims) verifiedClaims {
	displayName := raw.Name
	if displayName == "" {
		displayName = raw.PreferredUsername
	}
	identityID := raw.IdentityID
	if identityID == "" {
		return verifiedClaims{}
	}
	return verifiedClaims{Subject: raw.Subject, IdentityID: identityID, TenantID: raw.TenantID, PersonID: raw.PersonID, DisplayName: displayName}
}

func normalizeAuthorization(claims verifiedClaims, expectedTenantID, expectedRoleConfigHash, expectedEnvironmentCode string, maxRoles int) (verifiedClaims, error) {
	if claims.Subject == "" || claims.Subject != strings.TrimSpace(claims.Subject) || claims.IdentityID == "" || claims.IdentityID != strings.TrimSpace(claims.IdentityID) || claims.TenantID != expectedTenantID || claims.AuthzRevision == 0 {
		return verifiedClaims{}, errors.New("CRM OIDC identity or authorization metadata is invalid")
	}
	// role_config_hash 不再从 Keycloak Token 读取；它绑定 CRM 内置目录版本，
	// 由本地配置写入会话，详细授权则来自上面的在线授权上下文。
	claims.RoleConfigHash = expectedRoleConfigHash
	// CRM 业务表和追加式审计表的操作者标识上限为 64 字节。应在认证阶段拒绝过长 subject，
	// 避免生成“能登录但首次写入才失败或被截断”的不完整会话。
	if len([]byte(claims.Subject)) > 64 {
		return verifiedClaims{}, errors.New("CRM OIDC subject exceeds the signed actor identifier boundary")
	}
	if claims.PersonID != "" && !validPMSPersonID(claims.PersonID) {
		return verifiedClaims{}, errors.New("CRM OIDC person_id is invalid")
	}
	if len(claims.Roles) == 0 || len(claims.Roles) > maxRoles {
		return verifiedClaims{}, errors.New("CRM OIDC role or permission set is invalid")
	}
	// 基础平台超级管理员在 CRM 应用中等价于 CRM 超级管理员：应用接入可能只返回
	// platform-super-admin，而不会额外携带 crm_super_admin 映射角色。这里做受控别名
	// 映射，后续仍严格按 CRM 目录重新计算权限，避免信任上游任意权限声明。
	platformSuperAdmin := false
	roleInputs := make([]string, 0, len(claims.Roles))
	for _, role := range claims.Roles {
		role = canonicalCRMRole(role)
		if role == "crm_super_admin" && slicesContains(claims.Roles, "platform-super-admin") {
			platformSuperAdmin = true
		}
		roleInputs = append(roleInputs, role)
	}
	canonicalInputScopes := append([]sharedauth.DataScope(nil), claims.DataScopes...)
	for index := range canonicalInputScopes {
		canonicalInputScopes[index].RoleCode = canonicalCRMRole(canonicalInputScopes[index].RoleCode)
	}
	canonicalInputRoles, err := normalizedSet(roleInputs, nil)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC roles: %w", err)
	}
	canonicalScopes, scopeDecision, err := sharedauthorization.ValidateScopes(canonicalInputScopes, canonicalInputRoles, expectedEnvironmentCode, claims.IdentityID, claims.PersonID)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC data scopes: %w", err)
	}
	manifest := platformcatalog.CRMManifest()
	knownRoles := make(map[string]struct{}, len(manifest.Roles))
	for _, role := range manifest.Roles {
		knownRoles[role.Code] = struct{}{}
	}
	roles, err := normalizedSet(canonicalInputRoles, knownRoles)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC roles: %w", err)
	}
	if len(claims.Permissions) == 0 {
		return verifiedClaims{}, errors.New("CRM OIDC role or permission set is invalid")
	}
	permissions, err := normalizedSet(claims.Permissions, nil)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC permissions: %w", err)
	}
	for _, permission := range permissions {
		if permission == "all" || !platformcatalog.HasPermission(manifest, permission) {
			return verifiedClaims{}, errors.New("CRM OIDC permission is outside the CRM application catalog")
		}
	}
	organizationIDs, err := normalizedBoundedSet(scopeDecision.OrganizationIDs, 100, 64)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC organization_ids: %w", err)
	}
	if claims.PrimaryOrgID != strings.TrimSpace(claims.PrimaryOrgID) {
		return verifiedClaims{}, errors.New("CRM OIDC primary_org_id has surrounding whitespace")
	}
	if claims.PrimaryOrgID != "" {
		if len([]byte(claims.PrimaryOrgID)) > 64 || !containsSorted(organizationIDs, claims.PrimaryOrgID) {
			return verifiedClaims{}, errors.New("CRM OIDC primary_org_id is not in the active organization set")
		}
	}
	_ = platformSuperAdmin // the controlled role alias does not expand permissions
	claims.Roles, claims.Permissions, claims.OrganizationIDs, claims.DataScopes = roles, permissions, organizationIDs, canonicalScopes
	return claims, nil
}

// canonicalCRMRole collapses retired CRM role codes at the authentication
// boundary. The authorization catalog exposes only the canonical roles, while
// existing sessions and historical platform bindings can continue to use the
// old codes until administrators reassign them.
func canonicalCRMRole(role string) string {
	switch strings.TrimSpace(role) {
	case "platform-super-admin":
		return "crm_super_admin"
	case "implementation_engineer":
		return "technician"
	case "technical_lead":
		return "technical_director"
	default:
		return strings.TrimSpace(role)
	}
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validPMSPersonID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character > 0x7f || !(character == '-' || character == '_' || character == '.' || character == ':' ||
			character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return false
		}
	}
	return true
}

func normalizedBoundedSet(values []string, maximumItems, maximumBytes int) ([]string, error) {
	if len(values) > maximumItems {
		return nil, errors.New("too many values")
	}
	result, err := normalizedSet(values, nil)
	if err != nil {
		return nil, err
	}
	for index, value := range result {
		if len([]byte(value)) > maximumBytes {
			return nil, errors.New("value exceeds byte limit")
		}
		if values[index] != value {
			return nil, errors.New("values are not in canonical sorted order")
		}
	}
	return result, nil
}

func containsSorted(values []string, expected string) bool {
	index := sort.SearchStrings(values, expected)
	return index < len(values) && values[index] == expected
}

func normalizedSet(values []string, allow map[string]struct{}) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return nil, errors.New("value is empty or has surrounding whitespace")
		}
		if allow != nil {
			if _, ok := allow[value]; !ok {
				return nil, errors.New("value is not recognized")
			}
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("duplicate value")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func sameAuthorization(left, right verifiedClaims) bool {
	if left.IdentityID != right.IdentityID || left.TenantID != right.TenantID || left.PersonID != right.PersonID || left.PrimaryOrgID != right.PrimaryOrgID || left.RoleConfigHash != right.RoleConfigHash || left.AuthzRevision != right.AuthzRevision || len(left.OrganizationIDs) != len(right.OrganizationIDs) || len(left.Roles) != len(right.Roles) || len(left.Permissions) != len(right.Permissions) {
		return false
	}
	for index := range left.OrganizationIDs {
		if left.OrganizationIDs[index] != right.OrganizationIDs[index] {
			return false
		}
	}
	for index := range left.Roles {
		if left.Roles[index] != right.Roles[index] {
			return false
		}
	}
	for index := range left.Permissions {
		if left.Permissions[index] != right.Permissions[index] {
			return false
		}
	}
	if len(left.DataScopes) != len(right.DataScopes) {
		return false
	}
	for index := range left.DataScopes {
		if left.DataScopes[index] != right.DataScopes[index] {
			return false
		}
	}
	return true
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

type backchannelTransport struct {
	base                http.RoundTripper
	public, backchannel *url.URL
}

func (t *backchannelTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// 只改写与公开 issuer 完全同源的请求，第三方 URL 继续走原始目标，避免把任意请求导向内网。
	if request.URL.Scheme != t.public.Scheme || request.URL.Host != t.public.Host {
		return t.base.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host = t.backchannel.Scheme, t.backchannel.Host
	return t.base.RoundTrip(clone)
}
