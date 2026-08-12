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
	"golang.org/x/oauth2"
)

type OIDCOptions struct {
	Issuer, BackchannelBaseURL, PlatformBaseURL, ClientID, ClientSecret, RedirectURI string
	Scopes                                                                           []string
}

type verifiedClaims struct {
	Subject, IdentityID, TenantID, PersonID, DisplayName, RoleConfigHash string
	PrimaryOrgID                                                         string
	OrganizationIDs                                                      []string
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
	config          oauth2.Config
	verifier        *oidc.IDTokenVerifier
	provider        *oidc.Provider
	httpClient      *http.Client
	platformBaseURL string
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
	return &platformOIDCClient{
		httpClient: httpClient, provider: provider,
		platformBaseURL: strings.TrimRight(options.PlatformBaseURL, "/"),
		verifier:        provider.Verifier(&oidc.Config{ClientID: options.ClientID}),
		config:          oauth2.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: options.RedirectURI, Scopes: options.Scopes},
	}, nil
}

func (c *platformOIDCClient) AuthorizationURL(state, nonce, verifier string) string {
	return c.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
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
	if raw.IdentityID != "" && raw.IdentityID != raw.Subject {
		return verifiedClaims{}, errors.New("CRM OIDC identity_id does not match sub")
	}
	claims := claimsFromPlatform(raw)
	claims.AccessToken = token.AccessToken
	claims.ExpiresAt = earliestExpiry(idToken.Expiry, token.Expiry)
	// Keycloak Token 只承载稳定身份和少量 OIDC 元数据。详细角色、权限和组织范围
	// 由基础平台授权上下文接口按当前快照返回，避免权限变更等待 Token 过期。
	contextClaims, err := c.AuthorizationContext(ctx, token.AccessToken)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("load CRM authorization context: %w", err)
	}
	if contextClaims.Subject != claims.Subject || contextClaims.IdentityID != claims.Subject || (claims.TenantID != "" && contextClaims.TenantID != claims.TenantID) {
		return verifiedClaims{}, errors.New("CRM authorization context identity does not match OIDC identity")
	}
	contextClaims.DisplayName = claims.DisplayName
	contextClaims.PersonID = firstNonEmpty(contextClaims.PersonID, claims.PersonID)
	contextClaims.ExpiresAt = claims.ExpiresAt
	contextClaims.AccessToken = token.AccessToken
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

type authorizationContextResponse struct {
	Subject     string   `json:"sub"`
	IdentityID  string   `json:"identity_id"`
	TenantID    string   `json:"tenant_id"`
	PersonID    string   `json:"person_id"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	DataScopes  []struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
	} `json:"data_scopes"`
	AuthorizationRevision uint64 `json:"authorization_revision"`
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
		return verifiedClaims{}, fmt.Errorf("authorization context returned HTTP %d", resp.StatusCode)
	}
	var raw authorizationContextResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return verifiedClaims{}, fmt.Errorf("decode CRM authorization context: %w", err)
	}
	if raw.Subject == "" || raw.IdentityID == "" || raw.IdentityID != raw.Subject || raw.TenantID == "" || raw.AuthorizationRevision == 0 {
		return verifiedClaims{}, errors.New("CRM authorization context identity or revision is invalid")
	}
	organizationIDs := make([]string, 0, len(raw.DataScopes))
	for _, scope := range raw.DataScopes {
		if scope.ScopeType == "ORG" && scope.ScopeID != "" {
			organizationIDs = append(organizationIDs, scope.ScopeID)
		}
	}
	return verifiedClaims{Subject: raw.Subject, IdentityID: raw.IdentityID, TenantID: raw.TenantID, PersonID: raw.PersonID, Roles: raw.Roles, Permissions: raw.Permissions, OrganizationIDs: organizationIDs, AuthzRevision: raw.AuthorizationRevision}, nil
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
		identityID = raw.Subject
	}
	return verifiedClaims{Subject: raw.Subject, IdentityID: identityID, TenantID: raw.TenantID, PersonID: raw.PersonID, DisplayName: displayName}
}

func normalizeAuthorization(claims verifiedClaims, expectedTenantID, expectedRoleConfigHash string, maxRoles int) (verifiedClaims, error) {
	if claims.Subject == "" || claims.Subject != strings.TrimSpace(claims.Subject) || claims.IdentityID != claims.Subject || claims.TenantID != expectedTenantID || claims.AuthzRevision == 0 {
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
		if role == "platform-super-admin" {
			platformSuperAdmin = true
			role = "crm_super_admin"
		}
		roleInputs = append(roleInputs, role)
	}
	manifest := platformcatalog.CRMManifest()
	knownRoles := make(map[string]struct{}, len(manifest.Roles))
	rolePermissions := make(map[string][]string, len(manifest.Roles))
	for _, role := range manifest.Roles {
		knownRoles[role.Code] = struct{}{}
		rolePermissions[role.Code] = role.Permissions
	}
	roles, err := normalizedSet(roleInputs, knownRoles)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC roles: %w", err)
	}
	var permissions []string
	if platformSuperAdmin {
		permissions = nil
	} else {
		if len(claims.Permissions) == 0 {
			return verifiedClaims{}, errors.New("CRM OIDC role or permission set is invalid")
		}
		permissions, err = normalizedSet(claims.Permissions, nil)
		if err != nil {
			return verifiedClaims{}, fmt.Errorf("CRM OIDC permissions: %w", err)
		}
		for _, permission := range permissions {
			if permission == "all" || !platformcatalog.HasPermission(manifest, permission) {
				return verifiedClaims{}, errors.New("CRM OIDC permission is outside the CRM application catalog")
			}
		}
	}
	organizationIDs, err := normalizedBoundedSet(claims.OrganizationIDs, 100, 64)
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
	expectedPermissions := make(map[string]struct{})
	// 权限必须恰好等于有效角色的权限并集；既不允许额外权限，也不接受缺项造成的策略歧义。
	for _, role := range roles {
		for _, permission := range rolePermissions[role] {
			expectedPermissions[permission] = struct{}{}
		}
	}
	if platformSuperAdmin {
		permissions = make([]string, 0, len(expectedPermissions))
		for permission := range expectedPermissions {
			permissions = append(permissions, permission)
		}
		sort.Strings(permissions)
	}
	if len(permissions) != len(expectedPermissions) {
		return verifiedClaims{}, errors.New("CRM OIDC permission set does not match the effective role mapping")
	}
	for _, permission := range permissions {
		if _, expected := expectedPermissions[permission]; !expected {
			return verifiedClaims{}, errors.New("CRM OIDC permission set does not match the effective role mapping")
		}
	}
	claims.Roles, claims.Permissions, claims.OrganizationIDs = roles, permissions, organizationIDs
	return claims, nil
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
	if left.Subject != right.Subject || left.IdentityID != right.IdentityID || left.TenantID != right.TenantID || left.PersonID != right.PersonID || left.PrimaryOrgID != right.PrimaryOrgID || left.RoleConfigHash != right.RoleConfigHash || left.AuthzRevision != right.AuthzRevision || len(left.OrganizationIDs) != len(right.OrganizationIDs) || len(left.Roles) != len(right.Roles) || len(left.Permissions) != len(right.Permissions) {
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
