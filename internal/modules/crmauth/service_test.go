package crmauth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type memoryRepo struct {
	login    *LoginTransaction
	sessions map[string]*Session
}

func newMemoryRepo() *memoryRepo { return &memoryRepo{sessions: map[string]*Session{}} }
func (r *memoryRepo) SaveLogin(_ context.Context, value *LoginTransaction) error {
	copy := *value
	r.login = &copy
	return nil
}
func (r *memoryRepo) ConsumeLogin(_ context.Context, hash string, now time.Time) (*LoginTransaction, error) {
	if r.login == nil || r.login.StateHash != hash || !r.login.ExpiresAt.After(now) {
		return nil, errNotFound
	}
	value := r.login
	r.login = nil
	return value, nil
}
func (r *memoryRepo) CreateSession(_ context.Context, value *Session) error {
	copy := *value
	r.sessions[value.SessionIDHash] = &copy
	return nil
}

// effectiveInteractiveAt 复刻数据库谓词 COALESCE(last_interactive_at, created_at)：
// 迁移前的存量行没有显式活动时间时以创建时间作为最后交互。
func effectiveInteractiveAt(value *Session) time.Time {
	if value.LastInteractiveAt != nil {
		return *value.LastInteractiveAt
	}
	return value.CreatedAt
}

// sessionActive 在测试存储中复现本地撤销、绝对期限及可选旧模式空闲检查。
// 参数：value为会话，now为时钟，idleTTL为旧模式窗口或中央模式零值。
// 返回值：会话满足本地检查时为真；中央有效性由服务继续核验。
func (r *memoryRepo) sessionActive(value *Session, now time.Time, idleTTL time.Duration) bool {
	return value != nil && value.RevokedAt == nil && value.ExpiresAt.After(now) && (idleTTL <= 0 || effectiveInteractiveAt(value).After(now.Add(-idleTTL)))
}

func (r *memoryRepo) FindSession(_ context.Context, hash string, now time.Time, idleTTL time.Duration) (*Session, error) {
	value := r.sessions[hash]
	if !r.sessionActive(value, now, idleTTL) {
		return nil, errNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepo) TouchSession(_ context.Context, hash string, now, checkedAt time.Time, idleTTL time.Duration) error {
	value := r.sessions[hash]
	if !r.sessionActive(value, now, idleTTL) {
		return errNotFound
	}
	value.LastSeenAt = now
	if !checkedAt.IsZero() {
		value.AuthorizationCheckedAt = checkedAt
	}
	return nil
}
func (r *memoryRepo) RecordActivity(_ context.Context, hash string, now time.Time, idleTTL time.Duration) (time.Time, error) {
	value := r.sessions[hash]
	if !r.sessionActive(value, now, idleTTL) {
		return time.Time{}, errNotFound
	}
	interactiveAt := now
	value.LastSeenAt, value.LastInteractiveAt = now, &interactiveAt
	return sessionIdleDeadlineAt(now, idleTTL, value.ExpiresAt), nil
}
func (r *memoryRepo) RefreshSessionTokens(_ context.Context, hash string, now time.Time, idleTTL time.Duration, refresh sessionRefreshFunc) (*Session, error) {
	value := r.sessions[hash]
	if !r.sessionActive(value, now, idleTTL) {
		return nil, errNotFound
	}
	snapshot := *value
	tokens, changed, err := refresh(&snapshot)
	if err != nil {
		return nil, err
	}
	if changed {
		expiresAt := tokens.AccessTokenExpiresAt
		snapshot.AccessTokenCipher, snapshot.RefreshTokenCipher, snapshot.AccessTokenExpiresAt = tokens.AccessTokenCipher, tokens.RefreshTokenCipher, &expiresAt
		*value = snapshot
	}
	return &snapshot, nil
}
func (r *memoryRepo) RevokeSession(_ context.Context, hash string, now time.Time) error {
	if value := r.sessions[hash]; value != nil {
		value.RevokedAt = &now
	}
	return nil
}
func (r *memoryRepo) RevokeSessionsForSubject(_ context.Context, tenantID, subject string, now time.Time) error {
	for _, value := range r.sessions {
		if value.TenantID == tenantID && value.PlatformUserID == subject {
			value.RevokedAt = &now
		}
	}
	return nil
}

type fakeOIDC struct {
	exchanged, current       verifiedClaims
	exchangeErr, userInfoErr error
	lastVerifier, lastNonce  string
	refreshResult            refreshedTokens
	refreshErr               error
	refreshCalls             int
	lastRefreshToken         string
}

type recordingBrokerVerifier struct {
	called bool
	claims verifiedClaims
	err    error
}

func (v *recordingBrokerVerifier) Verify(_ context.Context, claims verifiedClaims) error {
	v.called = true
	v.claims = claims
	return v.err
}

func (f *fakeOIDC) AuthorizationURL(state, nonce, verifier string) string {
	return "https://identity.example/authorize?state=" + state
}
func (f *fakeOIDC) Exchange(_ context.Context, _, verifier, nonce string) (verifiedClaims, error) {
	f.lastVerifier, f.lastNonce = verifier, nonce
	return f.exchanged, f.exchangeErr
}
func (f *fakeOIDC) UserInfo(_ context.Context, _ string) (verifiedClaims, error) {
	return f.current, f.userInfoErr
}
func (f *fakeOIDC) RefreshToken(_ context.Context, refreshToken string) (refreshedTokens, error) {
	f.refreshCalls++
	f.lastRefreshToken = refreshToken
	if f.refreshErr != nil {
		return refreshedTokens{}, f.refreshErr
	}
	return f.refreshResult, nil
}

func validClaims(now time.Time) verifiedClaims {
	return verifiedClaims{Subject: "user-1", IdentityID: "user-1", TenantID: "tenant-1", DisplayName: "User One", Roles: []string{"sales"}, Permissions: catalogRolePermissions("sales"), DataScopes: []sharedauth.DataScope{{RoleCode: "sales", ScopeType: "APPLICATION"}}, RoleConfigHash: "hash-1", AuthzRevision: 8, ExpiresAt: now.Add(10 * time.Minute), AccessToken: "access-token"}
}

func catalogRolePermissions(roleCode string) []string {
	for _, role := range platformcatalog.CRMManifest().Roles {
		if role.Code == roleCode {
			return append([]string(nil), role.Permissions...)
		}
	}
	return nil
}

func newTestService(t *testing.T, repo repository, oidc oidcClient, now time.Time, brokers ...brokerVerifier) *Service {
	t.Helper()
	service, err := NewService(repo, oidc, []byte(strings.Repeat("k", 32)), Options{TenantID: "tenant-1", RoleConfigHash: "hash-1", EnvironmentCode: "dev", SessionIdleTTL: 30 * time.Minute, SessionAbsoluteTTL: 8 * time.Hour, MaxRoles: 3}, brokers...)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service
}

func TestCompleteLoginReportsOnlyValidatedIdentityToBrokerGate(t *testing.T) {
	now := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	broker := &recordingBrokerVerifier{}
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims}, now, broker)
	start, err := service.BeginLogin(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if !broker.called || broker.claims.IdentityID != "user-1" || broker.claims.Subject != "user-1" || broker.claims.AccessToken != "access-token" {
		t.Fatalf("broker received %#v", broker)
	}
}

func TestBrokerReceiptFailureDoesNotBypassOrBlockValidatedCRMSession(t *testing.T) {
	now := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	broker := &recordingBrokerVerifier{err: errors.New("platform unavailable")}
	service := newTestService(t, repo, &fakeOIDC{exchanged: validClaims(now)}, now, broker)
	start, _ := service.BeginLogin(context.Background(), "/")
	result, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil || !broker.called || result.SessionToken == "" {
		t.Fatalf("CompleteLogin() = %#v, %v; broker=%#v", result, err, broker)
	}
	if _, err = service.Authenticate(context.Background(), result.SessionToken); err != nil {
		t.Fatalf("validated CRM session was not created: %v", err)
	}
}

func TestCRMSessionPersistsAndRestoresTrustedLoginIP(t *testing.T) {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.LoginIP = "203.0.113.9"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims}, now)
	start, err := service.BeginLogin(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(context.Background(), result.SessionToken)
	if err != nil || principal.LoginIP != "203.0.113.9" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

func TestCRMSessionCapturesLoginIPFromOIDCCallback(t *testing.T) {
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	service := newTestService(t, repo, &fakeOIDC{exchanged: validClaims(now)}, now)
	start, err := service.BeginLogin(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code", "125.120.19.87")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(context.Background(), result.SessionToken)
	if err != nil || principal.LoginIP != "125.120.19.87" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

// AUD-IP-2026：回调观察到的私网地址是合法来源，必须写入会话。
func TestCRMSessionKeepsPrivateCallbackLoginIP(t *testing.T) {
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	service := newTestService(t, repo, &fakeOIDC{exchanged: validClaims(now)}, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	result, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code", "172.18.0.17")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(context.Background(), result.SessionToken)
	if err != nil || principal.LoginIP != "172.18.0.17" {
		t.Fatalf("principal=%#v err=%v，私网回调地址应保留", principal, err)
	}
}

func TestAuthorizationMetadataFromTokenIsNotTrustedAsRoleDirectoryVersion(t *testing.T) {
	now := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	claims := validClaims(now)
	claims.RoleConfigHash = "stale"
	broker := &recordingBrokerVerifier{}
	service := newTestService(t, newMemoryRepo(), &fakeOIDC{exchanged: claims}, now, broker)
	start, _ := service.BeginLogin(context.Background(), "/")
	if _, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code"); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if !broker.called {
		t.Fatal("broker verification was not called after current authorization validation")
	}
}

func TestLoginStateIsSingleUseAndPKCESecretsAreEncrypted(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	oidc := &fakeOIDC{exchanged: validClaims(now)}
	service := newTestService(t, repo, oidc, now)
	start, err := service.BeginLogin(context.Background(), "/customer-opportunity/opportunities")
	if err != nil || !strings.Contains(start.AuthorizationURL, "state=") {
		t.Fatalf("BeginLogin() = %+v, %v", start, err)
	}
	if repo.login == nil || strings.Contains(string(repo.login.NonceCipher), "nonce") || strings.Contains(string(repo.login.CodeVerifierCipher), "verifier") {
		t.Fatal("login secrets must only be persisted as ciphertext")
	}
	state := strings.Split(start.AuthorizationURL, "state=")[1]
	result, err := service.CompleteLogin(context.Background(), state, "code-1")
	if err != nil || result.SessionToken == "" {
		t.Fatalf("CompleteLogin() = %+v, %v", result, err)
	}
	if oidc.lastVerifier == "" || oidc.lastNonce == "" {
		t.Fatal("PKCE verifier and nonce were not restored for validation")
	}
	if _, err := service.CompleteLogin(context.Background(), state, "replay"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestAuthenticateMapsVerifiedClaimsAndRejectsAuthorizationChange(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	oidc := &fakeOIDC{exchanged: claims, current: claims}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	result, _ := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	principal, err := service.Authenticate(context.Background(), result.SessionToken)
	if err != nil || principal.UserID != "user-1" || principal.PersonID != "" || principal.PrimaryOrgID != "" || len(principal.OrganizationIDs) != 0 || principal.ScopeMode != sharedauth.ScopeAll || !principal.HasPermission("customer.read") {
		t.Fatalf("Authenticate() = %+v, %v", principal, err)
	}
	service.now = func() time.Time { return now.Add(16 * time.Second) }
	oidc.current.AuthzRevision++
	if _, err := service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("changed authorization error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session error = %v", err)
	}
}

func TestPrincipalDoesNotGuessPersonIDFromOIDCSubject(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Subject = "platform-user-that-resembles-a-person-id"

	principal := principalFromClaims(claims)
	if principal.UserID != claims.Subject {
		t.Fatalf("UserID = %q, want OIDC subject %q", principal.UserID, claims.Subject)
	}
	if principal.PersonID != "" {
		t.Fatalf("PersonID = %q, want empty without an authoritative platform-to-PMS mapping", principal.PersonID)
	}
}

func TestPrincipalUsesOnlyExplicitSignedPMSPersonID(t *testing.T) {
	claims := validClaims(time.Now())
	claims.PersonID = "PMS-U10086"
	principal := principalFromClaims(claims)
	if principal.PersonID != "PMS-U10086" || principal.UserID != claims.Subject {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestPersonMappingChangeRevokesCRMServerSession(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.PersonID = "PMS-A"
	oidc := &fakeOIDC{exchanged: claims, current: claims}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	result, _ := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	service.now = func() time.Time { return now.Add(16 * time.Second) }
	oidc.current.PersonID = "PMS-B"
	if _, err := service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("person mapping change error = %v", err)
	}
}

func TestNormalizeAuthorizationRequiresCatalogBoundMetadata(t *testing.T) {
	now := time.Now()
	base := validClaims(now)
	tests := []struct {
		name   string
		mutate func(*verifiedClaims)
	}{
		{"tenant", func(c *verifiedClaims) { c.TenantID = "other" }},
		{"identity missing", func(c *verifiedClaims) { c.IdentityID = "" }},
		{"revision", func(c *verifiedClaims) { c.AuthzRevision = 0 }},
		{"subject width", func(c *verifiedClaims) { c.Subject = strings.Repeat("a", 65) }},
		{"person whitespace", func(c *verifiedClaims) { c.PersonID = " PMS-A" }},
		{"person width", func(c *verifiedClaims) { c.PersonID = strings.Repeat("a", 65) }},
		{"person unicode incompatible with ASCII schema", func(c *verifiedClaims) { c.PersonID = "人员-一" }},
		{"person invisible format character", func(c *verifiedClaims) { c.PersonID = "PMS-\u200bA" }},
		{"person unsupported punctuation", func(c *verifiedClaims) { c.PersonID = "PMS/A" }},
		{"unknown role", func(c *verifiedClaims) { c.Roles = []string{"administrator"} }},
		{"all permission", func(c *verifiedClaims) { c.Permissions = []string{"all"} }},
		{"duplicate permission", func(c *verifiedClaims) { c.Permissions = []string{"customer.read", "customer.read"} }},
		{"scope role", func(c *verifiedClaims) { c.DataScopes[0].RoleCode = "other" }},
		{"application scope id", func(c *verifiedClaims) { c.DataScopes[0].ScopeID = "unexpected" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := base
			claims.Roles = append([]string(nil), base.Roles...)
			claims.Permissions = append([]string(nil), base.Permissions...)
			claims.OrganizationIDs = append([]string(nil), base.OrganizationIDs...)
			claims.DataScopes = append([]sharedauth.DataScope(nil), base.DataScopes...)
			test.mutate(&claims)
			if _, err := normalizeAuthorization(claims, "tenant-1", "hash-1", "dev", 3); err == nil {
				t.Fatal("normalizeAuthorization() unexpectedly succeeded")
			}
		})
	}
}

func TestNormalizeAuthorizationAcceptsPlatformEffectivePermissionsWithoutRoleReDerivation(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Permissions = []string{"customer.read"}
	normalized, err := normalizeAuthorization(claims, "tenant-1", "hash-1", "dev", 3)
	if err != nil || !reflect.DeepEqual(normalized.Permissions, []string{"customer.read"}) {
		t.Fatalf("personal-exception permission result=%#v err=%v", normalized.Permissions, err)
	}
}

func TestOrganizationChangeRevokesCRMServerSession(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	oidc := &fakeOIDC{exchanged: claims, current: claims}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	result, _ := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	service.now = func() time.Time { return now.Add(16 * time.Second) }
	oidc.current.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: "ORG", ScopeID: "org-c", EnvironmentCode: "dev"}}
	if _, err := service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("organization change error = %v", err)
	}
}

func TestManagementRolesKeepTenantScopeWhileCarryingOrganizations(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Roles = []string{"sales_director"}
	claims.Permissions = catalogRolePermissions("sales_director")
	claims.DataScopes = []sharedauth.DataScope{{RoleCode: "sales_director", ScopeType: "APPLICATION"}}
	principal := principalFromClaims(claims)
	if principal.ScopeMode != sharedauth.ScopeAll || len(principal.OrganizationIDs) != 0 {
		t.Fatalf("management principal = %#v", principal)
	}
}

func TestNormalizeAuthorizationCanonicalizesRetiredEngineerRole(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Roles = []string{"implementation_engineer"}
	claims.Permissions = catalogRolePermissions("technician")
	claims.DataScopes = []sharedauth.DataScope{{RoleCode: "implementation_engineer", ScopeType: "ENVIRONMENT", ScopeID: "env-dev", EnvironmentCode: "dev"}}
	normalized, err := normalizeAuthorization(claims, "tenant-1", "hash-1", "dev", 3)
	if err != nil || len(normalized.Roles) != 1 || normalized.Roles[0] != "technician" {
		t.Fatalf("retired implementation_engineer should canonicalize to technician: claims=%+v err=%v", normalized, err)
	}
}

func TestNormalizeAuthorizationMapsPlatformSuperAdminToCRMSuperAdmin(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Roles = []string{"platform-super-admin"}
	claims.DataScopes = []sharedauth.DataScope{{RoleCode: "platform-super-admin", ScopeType: "APPLICATION"}}
	// 兼容角色别名，但不再从角色重新扩展权限；平台返回的权限仍须属于 CRM 目录。
	claims.Permissions = []string{"customer.read"}
	normalized, err := normalizeAuthorization(claims, "tenant-1", "hash-1", "dev", 3)
	if err != nil {
		t.Fatalf("platform-super-admin should map to crm_super_admin: %v", err)
	}
	if len(normalized.Roles) != 1 || normalized.Roles[0] != "crm_super_admin" {
		t.Fatalf("normalized roles = %#v, want crm_super_admin", normalized.Roles)
	}
	want := []string{"customer.read"}
	if !reflect.DeepEqual(normalized.Permissions, want) {
		t.Fatalf("normalized permissions = %#v, want CRM super-admin catalog", normalized.Permissions)
	}
}

func TestCRMSessionRestoresLoginUsernameSeparatelyFromDisplayName(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	claims := validClaims(now)
	claims.Username = "alice"
	repo := newMemoryRepo()
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims}, now)
	start, err := service.BeginLogin(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(context.Background(), result.SessionToken)
	if err != nil || principal.Username != "alice" || principal.DisplayName != "User One" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

// loginSession 完成一次 OIDC 登录并返回浏览器 Cookie 原文，供会话期限测试复用。
func loginSession(t *testing.T, service *Service) string {
	t.Helper()
	start, err := service.BeginLogin(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	return result.SessionToken
}

// 统一会话策略：绝对期限来自本地配置，不能再被 15 分钟访问令牌截短；refresh token 与
// 访问令牌到期时间必须随会话加密保存，供服务端提前刷新。
func TestSessionAbsoluteExpiryIsNotCappedByAccessTokenExpiry(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(15 * time.Minute)
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)

	session := repo.sessions[tokenHash(loginSession(t, service))]
	if session == nil {
		t.Fatal("login did not create a session")
	}
	if want := now.Add(8 * time.Hour); !session.ExpiresAt.Equal(want) {
		t.Fatalf("session absolute expiry = %s, want %s", session.ExpiresAt, want)
	}
	if session.LastInteractiveAt == nil || !session.LastInteractiveAt.Equal(now) {
		t.Fatalf("last_interactive_at = %v, want login time", session.LastInteractiveAt)
	}
	if session.AccessTokenExpiresAt == nil || !session.AccessTokenExpiresAt.Equal(claims.ExpiresAt) {
		t.Fatalf("access token expiry = %v, want %s", session.AccessTokenExpiresAt, claims.ExpiresAt)
	}
	if refreshToken, err := service.codec.decrypt(session.RefreshTokenCipher); err != nil || refreshToken != "refresh-token" {
		t.Fatalf("stored refresh token = %q, err = %v", refreshToken, err)
	}
}

// 提供方未签发 refresh token 时不能生成无法在线重验的长会话；回退到旧语义（令牌到期封顶）。
func TestLoginWithoutRefreshTokenKeepsAccessTokenCap(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(15 * time.Minute)
	claims.RefreshToken = ""
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)

	session := repo.sessions[tokenHash(loginSession(t, service))]
	if session == nil || !session.ExpiresAt.Equal(claims.ExpiresAt) {
		t.Fatalf("session = %#v, want expiry capped at %s", session, claims.ExpiresAt)
	}
	if len(session.RefreshTokenCipher) != 0 {
		t.Fatal("session without refresh token must not persist refresh ciphertext")
	}
}

// 显式活动推进空闲窗口；普通已认证请求只更新 last_seen_at，不能为空闲会话续期。
func TestActivitySlidesIdleWindowWhileOrdinaryRequestsDoNot(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(24 * time.Hour) // 覆盖整个会话，避免刷新干扰本用例
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)
	token := loginSession(t, service)

	// 登录后 29 分钟的普通请求仍有效，但不得推进 last_interactive_at。
	service.now = func() time.Time { return now.Add(29 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("ordinary request before idle deadline failed: %v", err)
	}
	if interactive := repo.sessions[tokenHash(token)].LastInteractiveAt; interactive == nil || !interactive.Equal(now) {
		t.Fatalf("ordinary request moved last_interactive_at to %v", interactive)
	}

	// 显式活动把空闲窗口从当前时间重新滑动 30 分钟。
	activity, err := service.RecordActivity(context.Background(), token)
	if err != nil || activity.IdleTimeout != 30*time.Minute || activity.Remaining != 30*time.Minute {
		t.Fatalf("RecordActivity() = %+v, %v", activity, err)
	}

	// 登录后 40 分钟（活动后 11 分钟）仍有效；若活动没有续期，30 分钟空闲期限早已过期。
	service.now = func() time.Time { return now.Add(40 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("session with explicit activity at 40m was rejected: %v", err)
	}
}

// 绝对期限不随活动滑动：跨过 8 小时后即使活动上报也不能复活会话。
func TestAbsoluteExpiryIsNotExtendedByActivity(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(24 * time.Hour)
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)
	token := loginSession(t, service)

	service.now = func() time.Time { return now.Add(8*time.Hour + time.Second) }
	if _, err := service.RecordActivity(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("absolute-expired session activity error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("absolute-expired session error = %v", err)
	}
}

// 空闲到期返回 401 且不写全账号撤销/不广播，也不能被活动接口复活。
func TestIdleExpiryRejectsSessionWithoutRevocationOrRevival(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(24 * time.Hour)
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)
	token := loginSession(t, service)

	service.now = func() time.Time { return now.Add(31 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle-expired Authenticate error = %v", err)
	}
	if _, err := service.RecordActivity(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle-expired RecordActivity error = %v", err)
	}
	if session := repo.sessions[tokenHash(token)]; session.RevokedAt != nil {
		t.Fatalf("idle expiry must not write a global revocation, got %v", session.RevokedAt)
	}
	service.now = func() time.Time { return now.Add(32 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revived session error = %v", err)
	}
}

// 访问令牌到期前用保存的 refresh token 刷新；并发/重复鉴权不重复消费一次性 refresh token。
func TestAccessTokenRefreshesBeforeExpiryUsingStoredRefreshToken(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(15 * time.Minute)
	claims.RefreshToken = "refresh-1"
	oidc := &fakeOIDC{exchanged: claims, current: claims, refreshResult: refreshedTokens{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresAt: now.Add(35 * time.Minute)}}
	service := newTestService(t, repo, oidc, now)
	token := loginSession(t, service)

	service.now = func() time.Time { return now.Add(15 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("Authenticate() after access token expiry = %v", err)
	}
	if oidc.refreshCalls != 1 || oidc.lastRefreshToken != "refresh-1" {
		t.Fatalf("refresh calls = %d, token = %q", oidc.refreshCalls, oidc.lastRefreshToken)
	}
	session := repo.sessions[tokenHash(token)]
	if session.AccessTokenExpiresAt == nil || !session.AccessTokenExpiresAt.Equal(now.Add(35*time.Minute)) {
		t.Fatalf("refreshed access token expiry = %v", session.AccessTokenExpiresAt)
	}
	if refreshToken, err := service.codec.decrypt(session.RefreshTokenCipher); err != nil || refreshToken != "refresh-2" {
		t.Fatalf("rotated refresh token = %q, err = %v", refreshToken, err)
	}
	if _, err := service.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("second Authenticate() = %v", err)
	}
	if oidc.refreshCalls != 1 {
		t.Fatalf("a second request consumed another refresh token: calls=%d", oidc.refreshCalls)
	}
}

// 在线重验被提供方拒绝时，仅在持久化令牌仍是那枚被拒令牌时受控刷新一次。
func TestRejectedAccessTokenRefreshesOnlyWhenStillStale(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(15 * time.Minute)
	claims.RefreshToken = "refresh-1"
	oidc := &fakeOIDC{exchanged: claims, current: claims, refreshResult: refreshedTokens{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresAt: now.Add(35 * time.Minute)}}
	service := newTestService(t, repo, oidc, now)
	token := loginSession(t, service)
	staleAccessToken, err := service.codec.decrypt(repo.sessions[tokenHash(token)].AccessTokenCipher)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, err := service.refreshSessionTokens(context.Background(), tokenHash(token), now.Add(16*time.Minute), staleAccessToken); err != nil {
		t.Fatalf("refresh stale token = %v", err)
	}
	if oidc.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d", oidc.refreshCalls)
	}
	// 另一个并发请求仍持有旧令牌：数据库令牌已更新，不能再消费新的 refresh token。
	if _, err := service.refreshSessionTokens(context.Background(), tokenHash(token), now.Add(16*time.Minute), staleAccessToken); err != nil {
		t.Fatalf("repeat refresh with stale token = %v", err)
	}
	if oidc.refreshCalls != 1 {
		t.Fatalf("stale concurrent refresh consumed another token: calls=%d", oidc.refreshCalls)
	}
}

// 提供方明确拒绝 refresh token（过期/撤销/重放）时撤销本地会话；临时不可用只返回 503 并保留会话。
func TestRefreshFailureClassification(t *testing.T) {
	tests := []struct {
		name       string
		refreshErr error
		wantErr    error
		wantStatus bool
	}{
		{name: "provider rejected", refreshErr: errOIDCRefreshRejected, wantErr: ErrUnauthenticated, wantStatus: false},
		{name: "temporary unavailable", refreshErr: errors.New("platform unavailable"), wantErr: ErrAuthorizationUnavailable, wantStatus: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
			repo := newMemoryRepo()
			claims := validClaims(now)
			claims.ExpiresAt = now.Add(15 * time.Minute)
			claims.RefreshToken = "refresh-1"
			oidc := &fakeOIDC{exchanged: claims, current: claims, refreshErr: test.refreshErr}
			service := newTestService(t, repo, oidc, now)
			token := loginSession(t, service)
			service.now = func() time.Time { return now.Add(16 * time.Minute) }
			_, err := service.Authenticate(context.Background(), token)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Authenticate() error = %v, want %v", err, test.wantErr)
			}
			session := repo.sessions[tokenHash(token)]
			if test.wantStatus && session.RevokedAt != nil {
				t.Fatal("temporary platform failure must not revoke the local session")
			}
			if !test.wantStatus && session.RevokedAt == nil {
				t.Fatal("rejected refresh token must revoke the local session")
			}
			if test.wantStatus {
				// 平台恢复后同一会话应能继续刷新成功，不需要用户重新登录。
				oidc.refreshErr = nil
				oidc.refreshResult = refreshedTokens{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresAt: now.Add(35 * time.Minute)}
				if _, err := service.Authenticate(context.Background(), token); err != nil {
					t.Fatalf("Authenticate() after platform recovery = %v", err)
				}
			}
		})
	}
}

// 认证主体携带权威空闲策略与期限，供 /auth/me 的 idle_timeout_seconds / idle_remaining_ms 使用；
// 显式活动后期限从活动时刻重新计算。
func TestAuthenticatePrincipalCarriesBoundedIdleDeadline(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(24 * time.Hour)
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, newMemoryRepo(), &fakeOIDC{exchanged: claims, current: claims}, now)
	token := loginSession(t, service)

	service.now = func() time.Time { return now.Add(10 * time.Minute) }
	principal, err := service.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	if principal.SessionIdleTimeout != 30*time.Minute || !principal.SessionIdleDeadline.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("principal idle = timeout %s deadline %s", principal.SessionIdleTimeout, principal.SessionIdleDeadline)
	}
	if _, err := service.RecordActivity(context.Background(), token); err != nil {
		t.Fatalf("RecordActivity() = %v", err)
	}
	principal, err = service.Authenticate(context.Background(), token)
	if err != nil || !principal.SessionIdleDeadline.Equal(now.Add(40*time.Minute)) {
		t.Fatalf("principal after activity = %+v, %v", principal, err)
	}
}

// 升级前存量行 last_interactive_at 为 NULL：必须按 created_at 计算空闲窗口，而不是立刻锁死在线会话。
func TestLegacySessionWithoutInteractiveTimestampUsesCreatedAt(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(24 * time.Hour)
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)
	token := loginSession(t, service)
	// 模拟迁移前的存量行：活动时间为 NULL，且没有 refresh token / 令牌到期时间。
	legacy := repo.sessions[tokenHash(token)]
	legacy.LastInteractiveAt = nil
	legacy.RefreshTokenCipher = nil
	legacy.AccessTokenExpiresAt = nil

	service.now = func() time.Time { return now.Add(29 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("legacy session within idle window was rejected: %v", err)
	}
	service.now = func() time.Time { return now.Add(31 * time.Minute) }
	if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("legacy session past idle window error = %v", err)
	}
}

// 剩余空闲时间取 min(最后活动+空闲, 绝对到期)，且不为负。
func TestSessionIdleDeadlineIsBoundedByAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	if got, want := sessionIdleDeadlineAt(now, 30*time.Minute, now.Add(8*time.Hour)), now.Add(30*time.Minute); !got.Equal(want) {
		t.Fatalf("idle-bounded deadline = %s, want %s", got, want)
	}
	if got, want := sessionIdleDeadlineAt(now, 30*time.Minute, now.Add(10*time.Minute)), now.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("absolute-bounded deadline = %s, want %s", got, want)
	}
}
