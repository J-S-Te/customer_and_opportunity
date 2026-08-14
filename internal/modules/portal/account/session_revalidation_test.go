package account

import (
	"context"
	"errors"
	"testing"
	"time"

	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type revalidationRepository struct {
	session         *Session
	link            *IdentityLink
	revoked         bool
	touched         bool
	linkVerified    bool
	authorizationAt time.Time
}

func (r *revalidationRepository) WithTransaction(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (*revalidationRepository) UpsertPendingLink(context.Context, *IdentityLink) (*IdentityLink, error) {
	return nil, errors.New("not used")
}
func (r *revalidationRepository) FindLink(context.Context, string, string) (*IdentityLink, error) {
	return r.link, nil
}
func (*revalidationRepository) ActivateLink(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*revalidationRepository) RevertActivation(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*revalidationRepository) CreateActivation(context.Context, *ActivationContext) error {
	return errors.New("not used")
}
func (*revalidationRepository) ConsumeActivation(context.Context, string, time.Time) (*ActivationContext, error) {
	return nil, errors.New("not used")
}
func (*revalidationRepository) CreateSession(context.Context, *Session) error {
	return errors.New("not used")
}
func (r *revalidationRepository) FindSession(context.Context, string, string, time.Time) (*Session, error) {
	return r.session, nil
}
func (*revalidationRepository) ListSessions(context.Context, string, string, time.Time) ([]Session, error) {
	return nil, errors.New("not used")
}
func (*revalidationRepository) FindOwnedSession(context.Context, string, string, string, time.Time) (*Session, error) {
	return nil, errors.New("not used")
}
func (*revalidationRepository) RevokeSession(context.Context, string, string, string, time.Time) error {
	return errors.New("not used")
}
func (r *revalidationRepository) RevokeSessionsForSubject(context.Context, string, string, time.Time) error {
	r.revoked = true
	return nil
}
func (r *revalidationRepository) TouchSession(_ context.Context, _, _ string, _ time.Time, checkedAt time.Time) error {
	r.touched, r.authorizationAt = true, checkedAt
	return nil
}
func (r *revalidationRepository) MarkLinkVerified(context.Context, string, uint64, uint64, time.Time) error {
	r.linkVerified = true
	return nil
}
func (*revalidationRepository) WriteAuthEvent(context.Context, *AuthEvent) error          { return nil }
func (*revalidationRepository) CreateSecurityEvent(context.Context, *SecurityEvent) error { return nil }
func (*revalidationRepository) ListSecurityEvents(context.Context, string, string, int) ([]SecurityEvent, error) {
	return nil, errors.New("not used")
}
func (*revalidationRepository) AcknowledgeSecurityEvent(context.Context, string, string, string, time.Time) error {
	return errors.New("not used")
}

type revalidationOIDC struct {
	claims Claims
	err    error
	calls  int
}

func (*revalidationOIDC) AuthorizationURL(string, string, string, string) (string, error) {
	return "", errors.New("not used")
}
func (*revalidationOIDC) ExchangeAndValidate(context.Context, string, string, string) (Claims, error) {
	return Claims{}, errors.New("not used")
}
func (o *revalidationOIDC) UserInfo(context.Context, string) (Claims, error) {
	o.calls++
	return o.claims, o.err
}

type passthroughProtector struct{}

func (passthroughProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}
func (passthroughProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type unusedRandom struct{}

func (unusedRandom) Bytes(int) ([]byte, error) { return nil, errors.New("not used") }

func revalidationFixture(now time.Time) (*Service, *revalidationRepository, *revalidationOIDC) {
	session := &Session{
		Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: hash("session-token"),
		PlatformUserID: "subject-a", CustomerID: 7, AuthzRevision: 3, RoleConfigHash: "catalog-v1",
		Roles: []string{"portal_customer"}, Permissions: []string{"project.read", "report.read"},
		DataScopes:        []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}},
		AccessTokenCipher: []byte("access-token"), AuthorizationCheckedAt: now.Add(-16 * time.Second),
		ExpiresAt: now.Add(time.Minute), AbsoluteExpiry: now.Add(time.Minute),
	}
	repo := &revalidationRepository{session: session, link: &IdentityLink{Model: database.Model{ID: 9, TenantID: "tenant-a"}, PlatformUserID: "subject-a", CustomerID: 7, Status: IdentityActive}}
	oidc := &revalidationOIDC{claims: Claims{Subject: "subject-a", IdentityID: "subject-a", TenantID: "tenant-a", Roles: []string{"portal_customer"}, Permissions: []string{"report.read", "project.read"}, DataScopes: []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}, RoleConfigHash: "catalog-v1", AuthzRevision: 3}}
	service := NewService(repo, oidc, nil, passthroughProtector{}, fixedClock{now: now}, unusedRandom{}, "catalog-v1", 15*time.Minute)
	return service, repo, oidc
}

func TestAuthenticateSessionRevalidatesCurrentAuthorization(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	value, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true)
	if err != nil || value.PlatformUserID != "subject-a" {
		t.Fatalf("AuthenticateSession() value=%#v err=%v", value, err)
	}
	if oidc.calls != 1 || !repo.touched || !repo.linkVerified || repo.revoked || !repo.authorizationAt.Equal(now) {
		t.Fatalf("revalidation state calls=%d touched=%t verified=%t revoked=%t checked=%v", oidc.calls, repo.touched, repo.linkVerified, repo.revoked, repo.authorizationAt)
	}
}

func TestAuthenticateSessionRevokesAllSubjectSessionsWhenAuthorizationChanges(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	oidc.claims.AuthzRevision = 4
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); !errors.Is(err, ErrInvalidLoginState) {
		t.Fatalf("changed authorization error=%v", err)
	}
	if !repo.revoked || repo.touched || repo.linkVerified {
		t.Fatalf("changed authorization was not fail-closed: %#v", repo)
	}
}

func TestAuthenticateSessionRevokesAllSubjectSessionsWhenDataScopesChange(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	service.environmentCode = "prod"
	scopes := []DataScope{{RoleCode: "portal_customer", ScopeType: "ENVIRONMENT", ScopeID: "env-prod", EnvironmentCode: "prod"}}
	repo.session.DataScopes = append([]DataScope(nil), scopes...)
	oidc.claims.DataScopes = append([]DataScope(nil), scopes...)
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); err != nil {
		t.Fatalf("unchanged data scopes error=%v", err)
	}

	// Data ranges are part of the authorization snapshot, even when the Portal
	// currently remains customer-bound through its identity link. A changed
	// scope must not be silently retained by an already-issued local session.
	oidc.claims.DataScopes[0].ScopeID = "customer-8"
	repo.session.AuthorizationCheckedAt = now.Add(-authorizationCheckInterval - time.Second)
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); !errors.Is(err, ErrInvalidLoginState) {
		t.Fatalf("changed data scope error=%v", err)
	}
	if !repo.revoked {
		t.Fatal("changed data scope did not revoke sessions")
	}
}

// P1-3：授权服务不可用时的陈旧窗口只放行只读请求；写请求必须在线复核。
func TestAuthenticateSessionStaleWindowIsReadOnly(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	repo.session.AuthorizationCheckedAt = now.Add(-20 * time.Second)
	oidc.err = sharedauthorization.ErrUnavailable

	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); err != nil {
		t.Fatalf("read request inside stale window error=%v", err)
	}
	repo.session.AuthorizationCheckedAt = now.Add(-20 * time.Second)
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", false); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("write request error=%v, want ErrAuthorizationUnavailable", err)
	}
}

func TestAuthenticateSessionSkipsUserInfoInsideCheckWindow(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	repo.session.AuthorizationCheckedAt = now.Add(-5 * time.Second)
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); err != nil {
		t.Fatal(err)
	}
	if oidc.calls != 0 || !repo.touched || !repo.authorizationAt.IsZero() {
		t.Fatalf("unnecessary UserInfo check calls=%d checked=%v", oidc.calls, repo.authorizationAt)
	}
}
