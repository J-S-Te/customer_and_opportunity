package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/testsupport/sessionmysql"
	"golang.org/x/oauth2"
)

type mysqlRotationOIDC struct {
	centralTestOIDC
	refresher *platformOIDCClient
}

// RefreshToken 使用真实OAuth客户端调用仅允许旧令牌消费一次的临时IdP。
// 参数：ctx为受限刷新上下文，refresh为服务器解密材料。
// 返回值：真实HTTP轮换结果；重复消费会返回确定拒绝。
func (o *mysqlRotationOIDC) RefreshToken(ctx context.Context, refresh string) (tokenRefreshResult, error) {
	return o.refresher.RefreshToken(ctx, refresh)
}

// UserInfo 模拟刷新成功后的权限服务不可用，不返回任何业务权限。
// 参数：ctx为原业务请求，access为轮换后的访问材料。
// 返回值：空快照和依赖错误。
func (o *mysqlRotationOIDC) UserInfo(context.Context, string) (verifiedClaims, error) {
	return verifiedClaims{}, ErrAuthorizationUnavailable
}

// TestCentralSessionRotationCheckpointMySQL 验证真实CRM迁移、行锁及权限失败后的令牌持久性。
// 参数：t为独立MySQL测试上下文，数据库只能来自受守卫的专用DSN。
// 返回值：无；并发重复轮换、旧权限续用、截止漂移或丢失材料即失败。
func TestCentralSessionRotationCheckpointMySQL(t *testing.T) {
	db := sessionmysql.Open(t, "../../../migrations", migrationplan.CRM, "qa_session_crm_20261101")
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
	expectation := central.Expectation{TenantID: "tenant-1", ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}
	client := &centralTestClient{state: central.Context{SessionID: "canonical-login", TenantID: "tenant-1", SubjectID: "user-1", ClientID: "crm-web", ApplicationCode: expectation.ApplicationCode, EnvironmentCode: "dev", PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000, AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds()}}
	oidc := &mysqlRotationOIDC{refresher: &platformOIDCClient{config: oauth2.Config{ClientID: "crm-web", Endpoint: oauth2.Endpoint{TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams}}, httpClient: server.Client()}}
	repo := NewGORMRepository(db)
	service, err := NewService(repo, oidc, []byte(strings.Repeat("k", 32)), Options{TenantID: "tenant-1", RoleConfigHash: "hash-1", EnvironmentCode: "dev", SessionTTL: 8 * time.Hour, MaxRoles: 3, CentralClient: client, CentralExpectation: expectation})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	claims := validClaims(now)
	session := Session{SessionIDHash: tokenHash("qa-opaque"), TenantID: "tenant-1", PlatformUserID: "user-1", OIDCSessionID: "qa-sid", RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision, ExpiresAt: now.Add(8 * time.Hour), AuthorizationCheckedAt: now, CreatedAt: now, LastSeenAt: now, AccessTokenExpiresAt: now.Add(10 * time.Second), CentralSessionID: "canonical-login"}
	for value, target := range map[string]*[]byte{"initial-refresh": &session.RefreshTokenCipher, "old-access": &session.AccessTokenCipher, "server-binding": &session.CentralBindingCipher} {
		*target, err = service.codec.encrypt(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct {
		value  any
		target *[]byte
	}{{[]string{}, &session.OrganizationIDsJSON}, {claims.Roles, &session.RolesJSON}, {claims.Permissions, &session.PermissionsJSON}, {claims.DataScopes, &session.DataScopesJSON}} {
		*pair.target, err = json.Marshal(pair.value)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	failures := make(chan error, 12)
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.Authenticate(context.Background(), "qa-opaque")
			failures <- err
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Errorf("must preserve dependency failure, got %v", err)
		}
	}
	var stored Session
	if err := db.Where("session_id_hash = ?", session.SessionIDHash).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	refresh, err := service.codec.decrypt(stored.RefreshTokenCipher)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || refresh != "rotated-refresh" || stored.RevokedAt != nil || !stored.ExpiresAt.Equal(session.ExpiresAt) || !stored.AuthorizationCheckedAt.Equal(time.Unix(1, 0).UTC()) {
		t.Fatalf("rotation checkpoint: calls=%d preserved=%v revoked=%v deadline=%v authorization_pending=%v", calls.Load(), refresh == "rotated-refresh", stored.RevokedAt != nil, stored.ExpiresAt.Equal(session.ExpiresAt), stored.AuthorizationCheckedAt.Equal(time.Unix(1, 0).UTC()))
	}
}
