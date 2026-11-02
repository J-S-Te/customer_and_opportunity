package account

import (
	"context"
	"errors"
	"testing"
	"time"

	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

type dependencyPortalRepository struct {
	*revalidationRepository
	fail string
}

// FindLink 模拟客户绑定查询故障，区别于确实没有客户绑定。
// 参数：ctx为原请求，tenant/subject为已核验门户边界。
// 返回值：原有绑定或依赖错误。
func (r *dependencyPortalRepository) FindLink(ctx context.Context, tenant, subject string) (*IdentityLink, error) {
	if r.fail == "link" {
		return nil, context.DeadlineExceeded
	}
	return r.revalidationRepository.FindLink(ctx, tenant, subject)
}

// TouchSession 模拟完成权限核验后的本地观测写入失败。
// 参数：ctx为原请求，tenant/hash为当前会话，seen/checked为已核验时间。
// 返回值：无；指定故障返回取消错误。
func (r *dependencyPortalRepository) TouchSession(ctx context.Context, tenant, hash string, seen, checked time.Time) error {
	if r.fail == "touch" {
		return context.Canceled
	}
	return r.revalidationRepository.TouchSession(ctx, tenant, hash, seen, checked)
}

// MarkLinkVerified 模拟身份仍有效但数据库核验时间写入失败。
// 参数：ctx为原请求，tenant/id为客户绑定，revision/now为已核验版本与时间。
// 返回值：无；指定故障返回暂时依赖错误。
func (r *dependencyPortalRepository) MarkLinkVerified(ctx context.Context, tenant string, id, revision uint64, now time.Time) error {
	if r.fail == "mark" {
		return context.DeadlineExceeded
	}
	return r.revalidationRepository.MarkLinkVerified(ctx, tenant, id, revision, now)
}

// TestPortalDependencyFailurePreservesCentralSession 验证刷新之后的查询或写入故障不会伪装成身份失效。
// 参数：t为固定时钟及独立中央窗口的服务层测试上下文。
// 返回值：无；依赖故障不返回503语义或错误撤销会话即失败。
func TestPortalDependencyFailurePreservesCentralSession(t *testing.T) {
	for _, stage := range []string{"link", "mark", "touch"} {
		t.Run(stage, func(t *testing.T) {
			now := time.Now().UTC()
			service, repo, _ := revalidationFixture(now)
			service.repo = &dependencyPortalRepository{revalidationRepository: repo, fail: stage}
			service.centralExpectation = central.Expectation{TenantID: "tenant-a", ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev"}
			service.centralClient = mysqlPortalCentral{state: central.Context{SessionID: "canonical", TenantID: "tenant-a", SubjectID: "subject-a", ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev", PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000, AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds()}}
			repo.session.CentralSessionID = "canonical"
			repo.session.CentralBindingCipher = []byte("test-binding")
			repo.session.AccessTokenExpiresAt = now.Add(5 * time.Minute)
			if _, err := service.AuthenticateSession(context.Background(), "tenant-a", "session-token", false); !errors.Is(err, ErrAuthorizationUnavailable) {
				t.Fatalf("dependency failure must remain unavailable, got %v", err)
			}
			if repo.revoked {
				t.Fatal("dependency failure revoked a valid identity")
			}
		})
	}
}
