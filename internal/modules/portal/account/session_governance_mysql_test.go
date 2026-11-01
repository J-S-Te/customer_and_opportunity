package account

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/testsupport/sessionmysql"
	"golang.org/x/oauth2"
)

type mysqlPortalCentral struct {
	central.Client
	state central.Context
}

// State 提供隔离数据库用例的权威窗口，不触碰真实平台。
// 参数：请求与测试绑定标记。
// 返回值：固定的原始八小时截止。
func (c mysqlPortalCentral) State(context.Context, string) (central.Context, error) {
	return c.state, nil
}

type mysqlPortalOIDC struct {
	revalidationOIDC
	config *oauth2.Config
}

// ExchangeWithCentral 满足真实服务端口；本用例仅验证已建立会话的刷新阶段。
// 参数：请求与未使用的授权码、PKCE、nonce和票据。
// 返回值：明确拒绝，防止测试替身被误当成成功登录。
func (o *mysqlPortalOIDC) ExchangeWithCentral(context.Context, string, string, string, string) (Claims, error) {
	return Claims{}, ErrInvalidLoginState
}

// RefreshToken 通过真实HTTP OAuth客户端消费一次性测试令牌。
// 参数：ctx为受限刷新上下文，refresh为本地测试材料。
// 返回值：轮换结果或依赖错误。
func (o *mysqlPortalOIDC) RefreshToken(ctx context.Context, refresh string) (RefreshedTokens, error) {
	token, err := o.config.TokenSource(ctx, &oauth2.Token{RefreshToken: refresh, Expiry: time.Now().Add(-time.Minute)}).Token()
	if err != nil {
		return RefreshedTokens{}, ErrAuthorizationUnavailable
	}
	return RefreshedTokens{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: token.Expiry}, nil
}

// TestCentralSessionRotationCheckpointMySQL 验证门户真实迁移和并发行锁不复用已消费的令牌。
// 参数：t为显式隔离库测试上下文；密文端口使用只含合成标记的测试替身。
// 返回值：无；重复消费、授权检查点丢失、客户变化或原截止漂移即失败。
func TestCentralSessionRotationCheckpointMySQL(t *testing.T) {
	db := sessionmysql.Open(t, "../../../../migrations", migrationplan.Portal, "qa_session_portal_20261101")
	now := time.Now().UTC().Truncate(time.Millisecond)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "bad form", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("refresh_token") != "initial-refresh" || calls.Add(1) != 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"rotated-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":300}`))
	}))
	defer server.Close()
	expectation := central.Expectation{TenantID: "tenant-a", ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev"}
	authority := mysqlPortalCentral{state: central.Context{SessionID: "canonical-login", TenantID: "tenant-a", SubjectID: "subject-a", ClientID: "portal-web", ApplicationCode: expectation.ApplicationCode, EnvironmentCode: "dev", PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000, AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds()}}
	repo := NewGORMRepository(db)
	repo.EnableCentralSessions()
	oidc := &mysqlPortalOIDC{config: &oauth2.Config{ClientID: "portal-web", Endpoint: oauth2.Endpoint{TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams}}}
	service := NewService(repo, oidc, nil, passthroughProtector{}, fixedClock{now: now}, unusedRandom{}, "catalog-v1", 8*time.Hour)
	service.centralClient = authority
	service.centralExpectation = expectation
	session := Session{Model: database.Model{TenantID: "tenant-a"}, PublicID: "qa-portal-session", SessionIDHash: hash("qa-opaque"), PlatformUserID: "subject-a", CustomerID: 7, AuthzRevision: 3, RoleConfigHash: "catalog-v1", Roles: []string{"portal_customer"}, Permissions: []string{"report.read"}, DataScopes: []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}, AccessTokenCipher: []byte("old-access"), RefreshTokenCipher: []byte("initial-refresh"), CentralBindingCipher: []byte("server-binding"), CentralSessionID: "canonical-login", AccessTokenExpiresAt: now.Add(10 * time.Second), AuthorizationCheckedAt: now, ExpiresAt: now.Add(8 * time.Hour), AbsoluteExpiry: now.Add(8 * time.Hour), LastSeenAt: now}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	failures := make(chan error, 12)
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.refreshCentralAccess(context.Background(), &session)
			failures <- err
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Errorf("rotation failed: %v", err)
		}
	}
	var stored Session
	if err := db.Where("session_id_hash = ?", session.SessionIDHash).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || string(stored.RefreshTokenCipher) != "rotated-refresh" || stored.RevokedAt != nil || stored.CustomerID != 7 || !stored.AbsoluteExpiry.Equal(session.AbsoluteExpiry) || !stored.AuthorizationCheckedAt.Equal(time.Unix(1, 0).UTC()) {
		t.Fatalf("rotation checkpoint: calls=%d preserved=%v revoked=%v customer=%d deadline=%v authorization_pending=%v", calls.Load(), string(stored.RefreshTokenCipher) == "rotated-refresh", stored.RevokedAt != nil, stored.CustomerID, stored.AbsoluteExpiry.Equal(session.AbsoluteExpiry), stored.AuthorizationCheckedAt.Equal(time.Unix(1, 0).UTC()))
	}
	if _, err := service.readCentral(context.Background(), &stored, false); err != nil && !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatal(err)
	}
}
