package crmauth

import (
	"context"
	"errors"
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
	return verifiedClaims{Subject: "user-1", TenantID: "tenant-1", DisplayName: "User One", PrimaryOrgID: "org-a", OrganizationIDs: []string{"org-a", "org-b"}, Roles: []string{"sales"}, Permissions: catalogRolePermissions("sales"), RoleConfigHash: "hash-1", AuthzRevision: 8, ExpiresAt: now.Add(10 * time.Minute), AccessToken: "access-token"}
}

func catalogRolePermissions(roleCode string) []string {
	for _, role := range platformcatalog.CRMManifest().Roles {
		if role.Code == roleCode {
			return append([]string(nil), role.Permissions...)
		}
	}
	return nil
}

func newTestService(t *testing.T, repo repository, oidc oidcClient, now time.Time) *Service {
	t.Helper()
	service, err := NewService(repo, oidc, []byte(strings.Repeat("k", 32)), Options{TenantID: "tenant-1", RoleConfigHash: "hash-1", SessionTTL: 15 * time.Minute, MaxRoles: 3})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service
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
	if err != nil || principal.UserID != "user-1" || principal.PersonID != "" || principal.PrimaryOrgID != "org-a" || len(principal.OrganizationIDs) != 2 || principal.ScopeMode != sharedauth.ScopeSelf || !principal.HasPermission("customer.read") {
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

func TestNormalizeAuthorizationRequiresExactCatalogMetadata(t *testing.T) {
	now := time.Now()
	base := validClaims(now)
	tests := []struct {
		name   string
		mutate func(*verifiedClaims)
	}{
		{"tenant", func(c *verifiedClaims) { c.TenantID = "other" }},
		{"role hash", func(c *verifiedClaims) { c.RoleConfigHash = "old" }},
		{"revision", func(c *verifiedClaims) { c.AuthzRevision = 0 }},
		{"subject width", func(c *verifiedClaims) { c.Subject = strings.Repeat("a", 65) }},
		{"person whitespace", func(c *verifiedClaims) { c.PersonID = " PMS-A" }},
		{"person width", func(c *verifiedClaims) { c.PersonID = strings.Repeat("a", 65) }},
		{"person unicode incompatible with ASCII schema", func(c *verifiedClaims) { c.PersonID = "人员-一" }},
		{"person invisible format character", func(c *verifiedClaims) { c.PersonID = "PMS-\u200bA" }},
		{"person unsupported punctuation", func(c *verifiedClaims) { c.PersonID = "PMS/A" }},
		{"unknown role", func(c *verifiedClaims) { c.Roles = []string{"administrator"} }},
		{"all permission", func(c *verifiedClaims) { c.Permissions = []string{"all"} }},
		{"known permission outside role", func(c *verifiedClaims) { c.Permissions = append(c.Permissions, "customer.merge") }},
		{"missing effective permission", func(c *verifiedClaims) { c.Permissions = c.Permissions[:len(c.Permissions)-1] }},
		{"duplicate permission", func(c *verifiedClaims) { c.Permissions = []string{"customer.read", "customer.read"} }},
		{"unsorted organizations", func(c *verifiedClaims) { c.OrganizationIDs = []string{"org-b", "org-a"} }},
		{"duplicate organization", func(c *verifiedClaims) { c.OrganizationIDs = []string{"org-a", "org-a"} }},
		{"primary outside organizations", func(c *verifiedClaims) { c.PrimaryOrgID = "org-c" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := base
			claims.Roles = append([]string(nil), base.Roles...)
			claims.Permissions = append([]string(nil), base.Permissions...)
			claims.OrganizationIDs = append([]string(nil), base.OrganizationIDs...)
			test.mutate(&claims)
			if _, err := normalizeAuthorization(claims, "tenant-1", "hash-1", 3); err == nil {
				t.Fatal("normalizeAuthorization() unexpectedly succeeded")
			}
		})
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
	oidc.current.OrganizationIDs = []string{"org-a", "org-c"}
	if _, err := service.Authenticate(context.Background(), result.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("organization change error = %v", err)
	}
}

func TestManagementRolesKeepTenantScopeWhileCarryingOrganizations(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Roles = []string{"sales_director"}
	claims.Permissions = catalogRolePermissions("sales_director")
	principal := principalFromClaims(claims)
	if principal.ScopeMode != sharedauth.ScopeAll || len(principal.OrganizationIDs) != 2 {
		t.Fatalf("management principal = %#v", principal)
	}
}

func TestNormalizeAuthorizationAcceptsImplementationEngineerRole(t *testing.T) {
	claims := validClaims(time.Now())
	claims.Roles = []string{"implementation_engineer"}
	claims.Permissions = catalogRolePermissions("implementation_engineer")
	if _, err := normalizeAuthorization(claims, "tenant-1", "hash-1", 3); err != nil {
		t.Fatalf("implementation_engineer should be a recognized CRM role: %v", err)
	}
}
