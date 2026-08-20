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
func (r *memoryRepo) FindSession(_ context.Context, hash string, now time.Time) (*Session, error) {
	value := r.sessions[hash]
	if value == nil || value.RevokedAt != nil || !value.ExpiresAt.After(now) {
		return nil, errNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *memoryRepo) TouchSession(_ context.Context, hash string, now, checkedAt time.Time) error {
	value := r.sessions[hash]
	if value == nil {
		return errNotFound
	}
	value.LastSeenAt = now
	if !checkedAt.IsZero() {
		value.AuthorizationCheckedAt = checkedAt
	}
	return nil
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
	service, err := NewService(repo, oidc, []byte(strings.Repeat("k", 32)), Options{TenantID: "tenant-1", RoleConfigHash: "hash-1", EnvironmentCode: "dev", SessionTTL: 15 * time.Minute, MaxRoles: 3}, brokers...)
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
