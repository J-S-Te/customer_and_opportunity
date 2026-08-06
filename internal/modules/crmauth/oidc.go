package crmauth

import (
	"context"
	"errors"
	"fmt"
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
	Issuer, BackchannelBaseURL, ClientID, ClientSecret, RedirectURI string
	Scopes                                                          []string
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
	Subject           string   `json:"sub"`
	IdentityID        string   `json:"identity_id"`
	Nonce             string   `json:"nonce"`
	TenantID          string   `json:"tenant_id"`
	PersonID          string   `json:"person_id"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	PrimaryOrgID      string   `json:"primary_org_id"`
	OrganizationIDs   []string `json:"organization_ids"`
	Roles             []string `json:"roles"`
	Permissions       []string `json:"permissions"`
	RoleConfigHash    string   `json:"role_config_hash"`
	AuthzRevision     uint64   `json:"authz_revision"`
	TokenUse          string   `json:"token_use"`
}

type platformOIDCClient struct {
	config     oauth2.Config
	verifier   *oidc.IDTokenVerifier
	provider   *oidc.Provider
	httpClient *http.Client
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
		verifier: provider.Verifier(&oidc.Config{ClientID: options.ClientID}),
		config:   oauth2.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: options.RedirectURI, Scopes: options.Scopes},
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
	return claims, nil
}

func (c *platformOIDCClient) UserInfo(ctx context.Context, accessToken string) (verifiedClaims, error) {
	if strings.TrimSpace(accessToken) == "" {
		return verifiedClaims{}, errors.New("CRM OIDC access token is missing")
	}
	oidcContext := oidc.ClientContext(ctx, c.httpClient)
	info, err := c.provider.UserInfo(oidcContext, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken, TokenType: "Bearer"}))
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("load CRM OIDC UserInfo: %w", err)
	}
	var raw platformClaims
	if err := info.Claims(&raw); err != nil {
		return verifiedClaims{}, fmt.Errorf("decode CRM OIDC UserInfo: %w", err)
	}
	if raw.IdentityID == "" || raw.IdentityID != raw.Subject {
		return verifiedClaims{}, errors.New("CRM OIDC UserInfo identity_id does not match sub")
	}
	return claimsFromPlatform(raw), nil
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
	return verifiedClaims{Subject: raw.Subject, IdentityID: identityID, TenantID: raw.TenantID, PersonID: raw.PersonID, DisplayName: displayName, PrimaryOrgID: raw.PrimaryOrgID, OrganizationIDs: raw.OrganizationIDs, Roles: raw.Roles, Permissions: raw.Permissions, RoleConfigHash: raw.RoleConfigHash, AuthzRevision: raw.AuthzRevision}
}

func normalizeAuthorization(claims verifiedClaims, expectedTenantID, expectedRoleConfigHash string, maxRoles int) (verifiedClaims, error) {
	if claims.Subject == "" || claims.Subject != strings.TrimSpace(claims.Subject) || claims.IdentityID != claims.Subject || claims.TenantID != expectedTenantID ||
		claims.RoleConfigHash == "" || claims.RoleConfigHash != expectedRoleConfigHash || claims.AuthzRevision == 0 {
		return verifiedClaims{}, errors.New("CRM OIDC identity or authorization metadata is invalid")
	}
	// CRM 业务表和追加式审计表的操作者标识上限为 64 字节。应在认证阶段拒绝过长 subject，
	// 避免生成“能登录但首次写入才失败或被截断”的不完整会话。
	if len([]byte(claims.Subject)) > 64 {
		return verifiedClaims{}, errors.New("CRM OIDC subject exceeds the signed actor identifier boundary")
	}
	if claims.PersonID != "" && !validPMSPersonID(claims.PersonID) {
		return verifiedClaims{}, errors.New("CRM OIDC person_id is invalid")
	}
	if len(claims.Roles) == 0 || len(claims.Roles) > maxRoles || len(claims.Permissions) == 0 {
		return verifiedClaims{}, errors.New("CRM OIDC role or permission set is invalid")
	}
	manifest := platformcatalog.CRMManifest()
	knownRoles := make(map[string]struct{}, len(manifest.Roles))
	rolePermissions := make(map[string][]string, len(manifest.Roles))
	for _, role := range manifest.Roles {
		knownRoles[role.Code] = struct{}{}
		rolePermissions[role.Code] = role.Permissions
	}
	roles, err := normalizedSet(claims.Roles, knownRoles)
	if err != nil {
		return verifiedClaims{}, fmt.Errorf("CRM OIDC roles: %w", err)
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
