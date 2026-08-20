package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type completeLoginRepository struct {
	revalidationRepository
	activation     *ActivationContext
	findLinkCalls  int
	createdSession *Session
	securityEvents []*SecurityEvent
	link           *IdentityLink
}

func (r *completeLoginRepository) ConsumeActivation(context.Context, string, time.Time) (*ActivationContext, error) {
	return r.activation, nil
}
func (r *completeLoginRepository) CreateActivation(context.Context, *ActivationContext) error {
	return nil
}
func (r *completeLoginRepository) FindLink(context.Context, string, string) (*IdentityLink, error) {
	r.findLinkCalls++
	if r.link != nil {
		return r.link, nil
	}
	return nil, ErrNotProvisioned
}
func (r *completeLoginRepository) CreateSession(_ context.Context, session *Session) error {
	r.createdSession = session
	return nil
}
func (r *completeLoginRepository) CreateSecurityEvent(_ context.Context, event *SecurityEvent) error {
	r.securityEvents = append(r.securityEvents, event)
	return nil
}

type completeLoginOIDC struct {
	claims Claims
	err    error
}

func (*completeLoginOIDC) AuthorizationURL(string, string, string, string) (string, error) {
	return "https://sso.example/auth", nil
}
func (o *completeLoginOIDC) ExchangeAndValidate(context.Context, string, string, string) (Claims, error) {
	if o.err != nil {
		return Claims{}, o.err
	}
	return o.claims, nil
}
func (*completeLoginOIDC) UserInfo(context.Context, string) (Claims, error) {
	return Claims{}, errors.New("not used")
}

type sequenceRandom struct{ next byte }

func (r *sequenceRandom) Bytes(size int) ([]byte, error) {
	value := make([]byte, size)
	for index := range value {
		r.next++
		value[index] = r.next
	}
	return value, nil
}

// linklessRepository 模拟生产仓储"无映射"行为：FindLink 返回未开通错误而非 nil 链接。
type linklessRepository struct {
	revalidationRepository
}

func (linklessRepository) FindLink(context.Context, string, string) (*IdentityLink, error) {
	return nil, ErrNotProvisioned
}

type portalInviteNoop struct{}

func (portalInviteNoop) Verify(context.Context, string) (VerifiedInvite, error) {
	return VerifiedInvite{}, errors.New("not used")
}
func (portalInviteNoop) Consume(context.Context, string, string) error {
	return errors.New("not used")
}

type recordingInvites struct {
	verified     VerifiedInvite
	consumeCalls []string
	consumeErr   error
}

func (r *recordingInvites) Verify(context.Context, string) (VerifiedInvite, error) {
	return r.verified, nil
}
func (r *recordingInvites) Consume(_ context.Context, _ string, subject string) error {
	r.consumeCalls = append(r.consumeCalls, subject)
	return r.consumeErr
}

func validPortalClaims(customerRef string) Claims {
	return Claims{Subject: "subject-a", IdentityID: "subject-a", TenantID: "tenant-a", Roles: []string{"portal_customer"}, Permissions: []string{"project.read", "report.read"}, DataScopes: []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}, RoleConfigHash: "catalog-v1", AuthzRevision: 3, ExpiresAt: time.Now().Add(10 * time.Minute), AccessToken: "access-token", CustomerRef: customerRef}
}

// Phase 4：平台绑定开启 + customer_ref 存在时，非邀请登录不再依赖本地 portal_identity_links。
func TestCompleteLoginPlatformBindingSkipsLocalLink(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash(""), ReturnPath: "/"}
	repository := &completeLoginRepository{activation: activation}
	claims := validPortalClaims("7")
	claims.LoginIP = "203.0.113.9"
	oidc := &completeLoginOIDC{claims: claims}
	service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())

	result, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if result.CustomerID != 7 {
		t.Fatalf("CustomerID = %d, want 7", result.CustomerID)
	}
	if repository.findLinkCalls != 1 {
		t.Fatalf("FindLink calls = %d, want 1 (双来源交叉校验)", repository.findLinkCalls)
	}
	if repository.createdSession == nil || repository.createdSession.CustomerID != 7 {
		t.Fatalf("created session = %#v", repository.createdSession)
	}
	if repository.createdSession.LoginIP != "203.0.113.9" {
		t.Fatalf("session login IP=%q", repository.createdSession.LoginIP)
	}
}

// customer_ref 非法时按未开通处理（fail closed），不落任何会话。
func TestCompleteLoginPlatformBindingRejectsInvalidCustomerRef(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash(""), ReturnPath: "/"}
	repository := &completeLoginRepository{activation: activation}
	oidc := &completeLoginOIDC{claims: validPortalClaims("not-a-number")}
	service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())

	if _, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{}); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("CompleteLogin() error = %v, want ErrNotProvisioned", err)
	}
	if repository.createdSession != nil {
		t.Fatal("session created for invalid customer ref")
	}
}

// 平台绑定路径的会话复验：customer_ref 与登录时边界不一致即撤销。
func platformBindingSession(now time.Time) *Session {
	return &Session{
		Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: hash("session-token"),
		PlatformUserID: "subject-a", CustomerID: 7, AuthzRevision: 3, RoleConfigHash: "catalog-v1",
		Roles: []string{"portal_customer"}, Permissions: []string{"project.read", "report.read"},
		DataScopes:        []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}},
		AccessTokenCipher: []byte("access-token"), AuthorizationCheckedAt: now.Add(-16 * time.Second),
		ExpiresAt: now.Add(time.Minute), AbsoluteExpiry: now.Add(time.Minute),
	}
}

func TestAuthenticateSessionPlatformBindingRevalidatesCustomerRef(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	t.Run("matching customer ref", func(t *testing.T) {
		repository := &revalidationRepository{session: platformBindingSession(now)}
		oidc := &revalidationOIDC{claims: validPortalClaims("7")}
		service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
		if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); err != nil {
			t.Fatalf("AuthenticateSession() error = %v", err)
		}
		if repository.linkVerified {
			t.Fatal("MarkLinkVerified called on platform binding path")
		}
	})
	t.Run("changed customer ref revokes", func(t *testing.T) {
		repository := &linklessRepository{revalidationRepository: revalidationRepository{session: platformBindingSession(now)}}
		oidc := &revalidationOIDC{claims: validPortalClaims("8")}
		service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
		if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); !errors.Is(err, ErrInvalidLoginState) {
			t.Fatalf("AuthenticateSession() error = %v, want ErrInvalidLoginState", err)
		}
		if !repository.revoked {
			t.Fatal("sessions were not revoked after customer ref change")
		}
	})
	t.Run("legacy path still requires active link", func(t *testing.T) {
		// FindLink 返回"未开通"（与生产仓储一致），会话复验必须按身份停用处理。
		repository := &completeLoginRepository{}
		repository.revalidationRepository = revalidationRepository{session: platformBindingSession(now)}
		oidc := &revalidationOIDC{claims: validPortalClaims("7")}
		service := NewService(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev")
		if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); !errors.Is(err, ErrIdentityDisabled) {
			t.Fatalf("AuthenticateSession() error = %v, want ErrIdentityDisabled", err)
		}
	})
}

// 双来源交叉校验：本地映射与平台 customer_ref 不一致时失败关闭，不落任何会话。
func TestCompleteLoginPlatformBindingRejectsLinkMismatch(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash(""), ReturnPath: "/"}
	repository := &completeLoginRepository{activation: activation, link: &IdentityLink{CustomerID: 8, Status: IdentityActive}}
	oidc := &completeLoginOIDC{claims: validPortalClaims("7")}
	service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
	if _, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{}); !errors.Is(err, ErrSubjectMismatch) {
		t.Fatalf("CompleteLogin() error = %v, want ErrSubjectMismatch", err)
	}
	if repository.createdSession != nil {
		t.Fatal("session created despite link/customer_ref mismatch")
	}
}

// 映射缺失（退役中）不阻断平台绑定路径；但管理端禁用必须失败关闭（禁用 saga 未收敛时
// 不得放行新登录）——2026-08-14 安全审查修复的回归。
func TestCompleteLoginPlatformBindingMissingLinkAllowed(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash(""), ReturnPath: "/"}
	repository := &completeLoginRepository{activation: activation}
	oidc := &completeLoginOIDC{claims: validPortalClaims("7")}
	service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
	if _, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{}); err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if repository.createdSession == nil || repository.createdSession.CustomerID != 7 {
		t.Fatalf("session = %#v", repository.createdSession)
	}
}

func TestCompleteLoginPlatformBindingRejectsDisabledLink(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash(""), ReturnPath: "/"}
	repository := &completeLoginRepository{activation: activation, link: &IdentityLink{CustomerID: 7, Status: IdentityDisabled}}
	oidc := &completeLoginOIDC{claims: validPortalClaims("7")}
	service := NewServiceWithOptions(repository, oidc, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
	if _, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{}); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("CompleteLogin() error = %v, want ErrNotProvisioned", err)
	}
	if repository.createdSession != nil {
		t.Fatal("session created despite disabled local link")
	}
}

// Phase 5：平台绑定路径下，邀请登录不再依赖本地映射；客户边界由 customer_ref 与
// 邀请 CustomerID 双匹配，邀请消费仍收敛到 CRM。
func TestInvitationLoginPlatformBinding(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	invites := &recordingInvites{verified: VerifiedInvite{TenantID: "tenant-a", ExpectedPlatformUserID: "subject-a", CustomerID: 7, ContactID: pointerTo(uint64(9))}}
	t.Run("begin skips local link", func(t *testing.T) {
		repository := &completeLoginRepository{}
		service := NewServiceWithOptions(repository, &completeLoginOIDC{}, invites, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
		if _, err := service.BeginInvitationLogin(context.Background(), "invite-token", "/home"); err != nil {
			t.Fatalf("BeginInvitationLogin() error = %v", err)
		}
		if repository.findLinkCalls != 0 {
			t.Fatalf("FindLink calls = %d, want 0", repository.findLinkCalls)
		}
	})
	t.Run("complete matches customer ref and consumes invite", func(t *testing.T) {
		activation := &ActivationContext{
			Model:                  database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"},
			ExpectedPlatformUserID: "subject-a", CustomerID: 7,
			NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"),
			InviteTokenHash: hash("invite-token"), InviteTokenCipher: []byte("invite-token"), ReturnPath: "/home",
		}
		repository := &completeLoginRepository{activation: activation}
		oidc := &completeLoginOIDC{claims: validPortalClaims("7")}
		service := NewServiceWithOptions(repository, oidc, invites, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
		result, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{})
		if err != nil {
			t.Fatalf("CompleteLogin() error = %v", err)
		}
		if result.CustomerID != 7 || repository.createdSession == nil {
			t.Fatalf("result = %#v session = %#v", result, repository.createdSession)
		}
		if repository.findLinkCalls != 1 {
			t.Fatalf("FindLink calls = %d, want 1 (双来源交叉校验)", repository.findLinkCalls)
		}
		if len(invites.consumeCalls) != 1 || invites.consumeCalls[0] != "subject-a" {
			t.Fatalf("invite consume calls = %v", invites.consumeCalls)
		}
	})
	t.Run("customer ref mismatch rejects invite login", func(t *testing.T) {
		activation := &ActivationContext{
			Model:                  database.Model{TenantID: "tenant-a", CreatedBy: "portal-login"},
			ExpectedPlatformUserID: "subject-a", CustomerID: 7,
			NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"),
			InviteTokenHash: hash("invite-token"), InviteTokenCipher: []byte("invite-token"), ReturnPath: "/home",
		}
		repository := &completeLoginRepository{activation: activation}
		oidc := &completeLoginOIDC{claims: validPortalClaims("8")}
		service := NewServiceWithOptions(repository, oidc, invites, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", 15*time.Minute, "dev", WithPlatformBinding())
		if _, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{}); !errors.Is(err, ErrSubjectMismatch) {
			t.Fatalf("CompleteLogin() error = %v, want ErrSubjectMismatch", err)
		}
		if repository.createdSession != nil {
			t.Fatal("session created for customer ref mismatch")
		}
	})
}

func pointerTo(value uint64) *uint64 { return &value }
