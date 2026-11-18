package account

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"golang.org/x/oauth2"
)

type checkpointPortalRepository struct{ *revalidationRepository }

// AttachActivationTicket 禁止检查点测试误用登录桥接。
// 参数：登录事务材料不使用。
// 返回值：明确失效错误，不伪造登录成功。
func (*checkpointPortalRepository) AttachActivationTicket(context.Context, string, []byte, time.Time) (*ActivationContext, error) {
	return nil, ErrInvalidLoginState
}

// WithCentralSessionLock 在内存中提交第一阶段轮换，不与之后的授权核验共用事务。
// 参数：ctx为轮换上下文，租户和摘要限定会话，now为时钟，update为真实轮换逻辑。
// 返回值：已经保存的新材料或依赖错误。
func (r *checkpointPortalRepository) WithCentralSessionLock(_ context.Context, _, _ string, _ time.Time, update centralSessionUpdate) (*Session, error) {
	current := *r.session
	changed, err := update(&current)
	if err != nil {
		return nil, err
	}
	if changed {
		r.session = &current
	}
	returned := current
	return &returned, nil
}

// TestFreshPortalFailureKeepsCommittedTokenRotation 验证真实HTTP刷新之后授权503不会丢失新材料。
// 参数：t为内存会话和本地OAuth端点测试上下文，不使用任何DSN。
// 返回值：无；单次材料重复消费、检查点丢失或原期限变化时失败。
func TestFreshPortalFailureKeepsCommittedTokenRotation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	service, original, claims := revalidationFixture(now)
	repo := &checkpointPortalRepository{original}
	service.repo = repo
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil || request.Form.Get("refresh_token") != "initial-refresh" || calls.Add(1) != 1 {
			http.Error(w, "invalid_grant", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"rotated-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":300}`))
	}))
	defer provider.Close()
	oidc := &mysqlPortalOIDC{revalidationOIDC: *claims, config: &oauth2.Config{ClientID: "test-portal-web", Endpoint: oauth2.Endpoint{TokenURL: provider.URL}}}
	oidc.err = sharedauthorization.ErrUnavailable
	service.oidc = oidc
	service.centralClient = mysqlPortalCentral{state: central.Context{SessionID: "central", TenantID: "tenant-a", SubjectID: "subject-a", ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev", PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000, AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds()}}
	service.centralExpectation = central.Expectation{TenantID: "tenant-a", ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev"}
	repo.session.AccessTokenExpiresAt, repo.session.RefreshTokenCipher = now.Add(10*time.Second), []byte("initial-refresh")
	repo.session.CentralSessionID, repo.session.CentralBindingCipher = "central", []byte("binding")
	deadline := repo.session.AbsoluteExpiry
	ctx := authorizationrefresh.WithForce(context.Background())
	if _, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("authorization failure=%v", err)
	}
	if calls.Load() != 1 || string(repo.session.RefreshTokenCipher) != "rotated-refresh" || repo.revoked || !repo.session.AbsoluteExpiry.Equal(deadline) || !repo.session.AuthorizationCheckedAt.Equal(time.Unix(1, 0).UTC()) {
		t.Fatal("rotation checkpoint was rolled back or lifecycle changed")
	}
	oidc.err = nil
	if _, err := service.AuthenticateSession(ctx, "tenant-a", "session-token", true); err != nil || calls.Load() != 1 {
		t.Fatalf("recovery repeated rotation: error=%v count=%d", err, calls.Load())
	}
}
