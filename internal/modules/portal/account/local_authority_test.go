package account

import (
	"context"
	"errors"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"testing"
	"time"
)

type localAuthorityFixture struct {
	calls   int
	pending bool
	err     error
}

// ValidateCustomer 记录本地目录复核参数并返回受控错误。
// 参数：ctx/tenant/customer/subject为认证范围，pending为邀请激活意图。
// 返回值：测试指定的拒绝结果。
func (f *localAuthorityFixture) ValidateCustomer(_ context.Context, _ string, _ uint64, _ string, pending bool) error {
	f.calls++
	f.pending = pending
	return f.err
}

type transactionalInvitesFixture struct {
	calls int
	err   error
}

// Verify 提供邀请验证端口，不为未使用路径制造成功身份。
// 参数：请求上下文及原始邀请。
// 返回值：明确未使用错误。
func (*transactionalInvitesFixture) Verify(context.Context, string) (VerifiedInvite, error) {
	return VerifiedInvite{}, errors.New("not used")
}

// Consume 阻止本地邀请退回远端事务外消费。
// 参数：上下文、邀请和身份。
// 返回值：明确错误。
func (*transactionalInvitesFixture) Consume(context.Context, string, string) error {
	return errors.New("outside transaction")
}

// ConsumeInTransaction 记录真实事务端口调用。
// 参数：上下文、邀请及平台身份。
// 返回值：测试指定错误。
func (f *transactionalInvitesFixture) ConsumeInTransaction(context.Context, string, string) error {
	f.calls++
	return f.err
}

type localLoginRepository struct {
	completeLoginRepository
	activated bool
}

// ActivateLink 记录待激活映射的事务推进。
// 参数：原租户、映射、授权版本、身份及服务器时钟。
// 返回值：无。
func (r *localLoginRepository) ActivateLink(context.Context, string, uint64, uint64, string, time.Time) error {
	r.activated = true
	return nil
}

// TestLocalAuthorityCannotBeBypassedByPlatformBinding 验证平台数字引用不能替代本地权威。
// 参数：t为测试上下文。
// 返回值：无；放行失效目录或管理员被套客户边界时失败。
func TestLocalAuthorityCannotBeBypassedByPlatformBinding(t *testing.T) {
	now := time.Now().UTC()
	service, repo, _ := revalidationFixture(now)
	authority := &localAuthorityFixture{err: errors.New("local access disabled")}
	WithPlatformBinding()(service)
	WithLocalCustomerAuthority(authority)(service)
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); !errors.Is(err, ErrIdentityDisabled) || authority.calls != 1 {
		t.Fatalf("local authority bypassed: %v calls=%d", err, authority.calls)
	}
	repo.session.Roles = []string{"portal_super_admin"}
	repo.session.CustomerID = 0
	repo.session.AuthorizationCheckedAt = now
	authority.calls = 0
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", false); err != nil || authority.calls != 0 {
		t.Fatalf("management session forced into customer ACL: %v", err)
	}
}

// TestLocalInvitationConsumesInsideLoginTransaction 验证本地激活、会话与邀请采用同一事务端口。
// 参数：t为测试上下文。
// 返回值：无；事务外消费或忽略邀请错误时失败。
func TestLocalInvitationConsumesInsideLoginTransaction(t *testing.T) {
	now := time.Now().UTC()
	claims := validPortalClaims("portal-local:v1:7:subject-a")
	activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash("invite"), InviteTokenCipher: []byte("invite"), ExpectedPlatformUserID: "subject-a", CustomerID: 7, ReturnPath: "/"}
	repo := &localLoginRepository{completeLoginRepository: completeLoginRepository{activation: activation, link: &IdentityLink{Model: database.Model{ID: 1, TenantID: "tenant-a"}, CustomerID: 7, PlatformUserID: "subject-a", Status: IdentityPending}}}
	invites := &transactionalInvitesFixture{err: errors.New("invitation conflict")}
	authority := &localAuthorityFixture{}
	service := NewServiceWithOptions(repo, &completeLoginOIDC{claims: claims}, invites, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", time.Hour, "dev", WithPlatformBinding(), WithLocalCustomerAuthority(authority))
	if _, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{}); !errors.Is(err, invites.err) || invites.calls != 1 || !repo.activated || !authority.pending {
		t.Fatalf("transactional invite not enforced: %v calls=%d", err, invites.calls)
	}
}

// ResolveCustomerRef 模拟本地权威对精确版本引用与已认证主体的解析。
// 参数：ctx/tenant/subject/ref为可信认证边界，pending为邀请激活意图。
// 返回值：本地客户主键；CRM数字或错误主体拒绝。
func (f *localAuthorityFixture) ResolveCustomerRef(_ context.Context, tenant, subject, ref string, pending bool) (uint64, error) {
	if tenant != "tenant-a" || ref != "portal-local:v1:7:"+subject || ref == "portal-local:v1:7:" {
		return 0, ErrNotProvisioned
	}
	return 7, nil
}

// TestLocalReferenceLoginAndOnlineRevalidation 验证LOCAL登录与在线刷新不会接受CRM数字或其他主体。
// 参数：t为测试上下文。
// 返回值：无；安全引用不被解析或非法引用进入会话时失败。
func TestLocalReferenceLoginAndOnlineRevalidation(t *testing.T) {
	for _, usePlatform := range []bool{false, true} {
		for _, ref := range []string{"portal-local:v1:7:subject-a", "7", "portal-local:v1:7:subject-b", ""} {
			now := time.Now().UTC()
			claims := validPortalClaims(ref)
			activation := &ActivationContext{Model: database.Model{TenantID: "tenant-a"}, NonceHash: hash("nonce"), NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"), InviteTokenHash: hash(""), ReturnPath: "/"}
			repo := &localLoginRepository{completeLoginRepository: completeLoginRepository{activation: activation, link: &IdentityLink{Model: database.Model{ID: 1, TenantID: "tenant-a"}, CustomerID: 7, PlatformUserID: "subject-a", Status: IdentityActive}}}
			service := NewServiceWithOptions(repo, &completeLoginOIDC{claims: claims}, portalInviteNoop{}, passthroughProtector{}, fixedClock{now: now}, &sequenceRandom{}, "catalog-v1", time.Hour, "dev", WithLocalCustomerAuthority(&localAuthorityFixture{}))
			service.usePlatformBinding = usePlatform
			_, err := service.CompleteLogin(context.Background(), "state", "code", LoginMetadata{})
			valid := ref == "portal-local:v1:7:subject-a"
			if valid && err != nil || !valid && (err == nil || repo.createdSession != nil) {
				t.Errorf("login platform=%t ref=%q err=%v", usePlatform, ref, err)
			}
			sessionService, sessionRepo, oidc := revalidationFixture(now)
			sessionService.usePlatformBinding = usePlatform
			WithLocalCustomerAuthority(&localAuthorityFixture{})(sessionService)
			oidc.claims.CustomerRef = ref
			_, err = sessionService.AuthenticateSession(context.Background(), "tenant-a", "session-token", false)
			if valid && err != nil || !valid && (err == nil || sessionRepo.touched) {
				t.Errorf("refresh platform=%t ref=%q err=%v", usePlatform, ref, err)
			}
		}
	}
}
