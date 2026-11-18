package account

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

// TestAuthorizationChangesPreservePortalLogin 验证合法的新门户授权原子替换当前会话。
// 参数：t为固定客户边界的旧模式与中央模式测试上下文。
// 返回值：无；权限范围未更新、注销或延长原期限时失败。
func TestAuthorizationChangesPreservePortalLogin(t *testing.T) {
	for _, useCentral := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "central"}[useCentral], func(t *testing.T) {
			now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
			service, repo, client := revalidationFixture(now)
			originalExpiry, originalAbsolute := repo.session.ExpiresAt, repo.session.AbsoluteExpiry
			if useCentral {
				service.centralClient = mysqlPortalCentral{state: central.Context{
					SessionID: "central", TenantID: "tenant-a", SubjectID: "subject-a",
					ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev",
					PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000,
					AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds(),
				}}
				service.centralExpectation = central.Expectation{
					TenantID: "tenant-a", ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev",
				}
				repo.session.CentralBindingCipher = []byte("binding")
				repo.session.CentralSessionID = "central"
				repo.session.AccessTokenExpiresAt = now.Add(5 * time.Minute)
			}
			client.claims.AuthzRevision = 4
			client.claims.Permissions = []string{"project.read"}
			client.claims.DataScopes = []DataScope{{RoleCode: "portal_customer", ScopeType: "SELF", ScopeID: "subject-a", EnvironmentCode: "dev"}}
			session, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true)
			if err != nil {
				t.Fatalf("valid changed authorization must preserve login: %v", err)
			}
			if session.AuthzRevision != 4 || !reflect.DeepEqual(session.Permissions, []string{"project.read"}) || !reflect.DeepEqual(session.DataScopes, client.claims.DataScopes) || session.CustomerID != 7 {
				t.Fatalf("authorization or customer boundary is stale: %#v", session)
			}
			if repo.revoked || !repo.session.ExpiresAt.Equal(originalExpiry) || !repo.session.AbsoluteExpiry.Equal(originalAbsolute) {
				t.Fatal("authorization refresh changed the original session lifecycle")
			}
			client.err = context.Canceled
			session, err = service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true)
			if err != nil || session.AuthzRevision != 4 || len(session.Permissions) != 1 {
				t.Fatalf("persisted authorization was not restored: %#v %v", session, err)
			}
		})
	}
}

// WithAuthorizationLock 用内存事务提交认证器产生的授权快照，故障不写回。
// 参数：ctx为请求，tenant和hash为会话边界，now为时钟，update为持锁核验操作。
// 返回值：已提交快照或认证、授权、依赖错误。
func (r *revalidationRepository) WithAuthorizationLock(ctx context.Context, tenant, hash string,
	now time.Time, update func(context.Context, *Session) error) (*Session, error) {
	current := *r.session
	err := update(ctx, &current)
	if errors.Is(err, ErrInvalidLoginState) || errors.Is(err, ErrInvalidClaims) {
		r.revoked = true
		return nil, err
	}
	if err != nil && !errors.Is(err, ErrPortalAuthorization) {
		return nil, err
	}
	r.session = &current
	r.authorizationAt = current.AuthorizationCheckedAt
	return &current, err
}

// TestFreshPortalReadPreservesDeadlinesAndNeverUsesStale 验证窗口内强制读取不续空闲且失败返回503。
// 参数：t为旧模式会话及客户绑定测试上下文。
// 返回值：无；范围未更新、后台续期或依赖故障回退旧权限时失败。
func TestFreshPortalReadPreservesDeadlinesAndNeverUsesStale(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	repo.session.AuthorizationCheckedAt = now.Add(-time.Second)
	seen, expiry, absolute := repo.session.LastSeenAt, repo.session.ExpiresAt, repo.session.AbsoluteExpiry
	oidc.claims.Permissions = []string{"project.read"}
	// 同一revision的自然时效变化也必须读取，不以revision是否变化决定缓存。
	ctx := authorizationrefresh.WithForce(context.Background())
	session, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true)
	if err != nil || !reflect.DeepEqual(session.Permissions, []string{"project.read"}) {
		t.Fatalf("fresh=%#v %v", session, err)
	}
	if !repo.session.LastSeenAt.Equal(seen) || !repo.session.ExpiresAt.Equal(expiry) || !repo.session.AbsoluteExpiry.Equal(absolute) {
		t.Fatal("fresh read extended lifecycle")
	}
	for _, failure := range []error{sharedauthorization.ErrUnavailable, context.Canceled} {
		oidc.err = failure
		if _, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("fresh failure=%v", err)
		}
		if repo.revoked {
			t.Fatal("dependency failure revoked session")
		}
	}
}

// TestPortalRevocationClearsProjectionAndCanRecover 验证门户全撤销不复用旧权限且保留再授权能力。
// 参数：t为真实门户服务与同一Cookie测试上下文。
// 返回值：无；旧范围残留、403误分类或有效再授权失败时失败。
func TestPortalRevocationClearsProjectionAndCanRecover(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	service, repo, oidc := revalidationFixture(now)
	ctx := authorizationrefresh.WithForce(context.Background())
	oidc.err = sharedauthorization.ErrForbidden
	if _, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true); !errors.Is(err, ErrPortalAuthorization) {
		t.Fatalf("denial=%v", err)
	}
	if repo.revoked || len(repo.session.Permissions) != 0 || len(repo.session.DataScopes) != 0 {
		t.Fatal("denial retained authorization or cleared login")
	}
	if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", true); !errors.Is(err, ErrPortalAuthorization) {
		t.Fatalf("ordinary denied read=%v", err)
	}
	oidc.err = nil
	oidc.claims.AuthzRevision = 5
	session, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true)
	if err != nil || session.AuthzRevision != 5 || session.CustomerID != 7 {
		t.Fatalf("same-cookie recovery=%#v %v", session, err)
	}
}

// TestPortalRefreshUpdatesLocalCatalogBinding 验证兼容目录升级后的原会话绑定更新为当前目录。
// 参数：t为同身份、同客户、当前目录在线授权测试上下文。
// 返回值：无；新角色范围生效却保留旧目录哈希时失败。
func TestPortalRefreshUpdatesLocalCatalogBinding(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	service, repo, _ := revalidationFixture(now)
	repo.session.RoleConfigHash = "previous-compatible-catalog"
	session, err := service.AuthenticateSession(authorizationrefresh.WithForce(context.Background()), "tenant-a", "session-token", true)
	if err != nil || session.RoleConfigHash != "catalog-v1" {
		t.Fatalf("catalog binding=%#v error=%v", session, err)
	}
}

// TestFreshPortalLocalDependencyFailureIsUnavailable 验证旧模式刷新首查与客户映射故障不伪装为退出。
// 参数：t为本地数据库与客户映射故障测试上下文。
// 返回值：无；强制读取的未知依赖错误未返回503语义时失败。
func TestFreshPortalLocalDependencyFailureIsUnavailable(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	ctx := authorizationrefresh.WithForce(context.Background())
	t.Run("lookup", func(t *testing.T) {
		service := &Service{repo: unavailableSessionRepository{}, clock: fixedClock{now: now}}
		if _, err := service.AuthenticateSession(ctx, "tenant", "opaque", true); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("lookup failure=%v", err)
		}
	})
	t.Run("link", func(t *testing.T) {
		service, repo, _ := revalidationFixture(now)
		service.repo = &dependencyPortalRepository{revalidationRepository: repo, fail: "link"}
		if _, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("link failure=%v", err)
		}
		if repo.revoked {
			t.Fatal("dependency failure revoked portal login")
		}
	})
}
