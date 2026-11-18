package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

// TestAuthorizationChangesPreserveCRMLogin 验证合法的新授权替换原会话而不注销。
// 参数：t为旧模式与中央模式的内存认证测试上下文。
// 返回值：无；权限未缩小、持久化丢失或会话期限变化时失败。
func TestAuthorizationChangesPreserveCRMLogin(t *testing.T) {
	for _, useCentral := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "central"}[useCentral], func(t *testing.T) {
			now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
			repo := newMemoryRepo()
			claims := validClaims(now)
			client := &fakeOIDC{exchanged: claims, current: claims}
			service := newTestService(t, repo, client, now)
			start, err := service.BeginLogin(context.Background(), "/")
			if err != nil {
				t.Fatal(err)
			}
			login, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
			if err != nil {
				t.Fatal(err)
			}
			stored := repo.sessions[tokenHash(login.SessionToken)]
			originalExpiry, originalInteractive := stored.ExpiresAt, *stored.LastInteractiveAt
			if useCentral {
				service.options.CentralClient = &centralTestClient{state: central.Context{
					SessionID: "central", TenantID: "tenant-1", SubjectID: "user-1",
					ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev",
					PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000,
					AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds(),
				}}
				service.options.CentralExpectation = central.Expectation{
					TenantID: "tenant-1", ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev",
				}
				stored.CentralBindingCipher, err = service.codec.encrypt("binding")
				if err != nil {
					t.Fatal(err)
				}
				stored.CentralSessionID = "central"
			}
			service.now = func() time.Time { return now.Add(16 * time.Second) }
			client.current.Roles = []string{"technician"}
			client.current.Permissions = []string{"customer.read"}
			client.current.DataScopes = []sharedauth.DataScope{{RoleCode: "technician", ScopeType: "SELF", ScopeID: "user-1", EnvironmentCode: "dev"}}
			client.current.AuthzRevision = 9
			principal, err := service.Authenticate(context.Background(), login.SessionToken)
			if err != nil {
				t.Fatalf("valid changed authorization must preserve login: %v", err)
			}
			if !reflect.DeepEqual(principal.Roles, []string{"technician"}) || len(principal.Permissions) != 1 || !principal.HasPermission("customer.read") || principal.ScopeMode != sharedauth.ScopeSelf || principal.AuthzRevision != 9 {
				t.Fatalf("authorization was not replaced: %#v", principal)
			}
			persisted := repo.sessions[tokenHash(login.SessionToken)]
			if persisted.RevokedAt != nil || !persisted.ExpiresAt.Equal(originalExpiry) || !persisted.LastInteractiveAt.Equal(originalInteractive) {
				t.Fatal("authorization refresh changed the original session lifecycle")
			}
			service.now = func() time.Time { return now.Add(17 * time.Second) }
			principal, err = service.Authenticate(context.Background(), login.SessionToken)
			if err != nil || principal.AuthzRevision != 9 || principal.HasPermission("customer.create") {
				t.Fatalf("persisted authorization was not restored: %#v %v", principal, err)
			}
		})
	}
}

// TestCRMRefreshHeaderRevalidatesOnlySessionReads 验证刷新头只能对自描述读强制在线复核。
// 参数：t为真实认证中间件与内存会话的HTTP测试上下文。
// 返回值：无；窗口内新授权未读取、错误状态错误或业务路径被强制刷新时失败。
func TestCRMRefreshHeaderRevalidatesOnlySessionReads(t *testing.T) {
	cases := []struct {
		name, path, header string
		failure            error
		status             int
		revision           uint64
	}{
		{"fresh", "/api/v1/auth/me", "1", nil, 200, 9},
		{"prefix", "/customer-opportunity/api/v1/auth/me", "1", nil, 200, 9},
		{"normal", "/api/v1/auth/me", "", nil, 200, 8},
		{"business", "/api/v1/customers", "1", nil, 200, 8},
		{"failure", "/api/v1/auth/me", "1", sharedauthorization.ErrUnavailable, 503, 0},
		{"denied", "/api/v1/auth/me", "1", sharedauthorization.ErrForbidden, 403, 0},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
			repo := newMemoryRepo()
			claims := validClaims(now)
			oidc := &fakeOIDC{exchanged: claims, current: claims}
			service := newTestService(t, repo, oidc, now)
			start, _ := service.BeginLogin(context.Background(), "/")
			login, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
			if err != nil {
				t.Fatal(err)
			}
			oidc.current.AuthzRevision, oidc.userInfoErr = 9, item.failure
			router := gin.New()
			router.GET(item.path, middleware.SessionAuth(service, "crm_session"), func(c *gin.Context) {
				principal, _ := sharedauth.FromContext(c.Request.Context())
				c.JSON(200, gin.H{"revision": principal.AuthzRevision})
			})
			request := httptest.NewRequest(http.MethodGet, item.path, nil)
			request.AddCookie(&http.Cookie{Name: "crm_session", Value: login.SessionToken})
			request.Header.Set("X-Authorization-Refresh", item.header)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != item.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, item.status, response.Body.String())
			}
			if item.status == 200 {
				var data struct {
					Revision uint64 `json:"revision"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				if data.Revision != item.revision {
					t.Fatalf("revision=%d want=%d", data.Revision, item.revision)
				}
			}
			if repo.sessions[tokenHash(login.SessionToken)].RevokedAt != nil {
				t.Fatal("refresh revoked the browser session")
			}
		})
	}
}

// WithAuthorizationLock 在内存仓储中提交真实认证逻辑产生的授权快照。
// 参数：ctx为请求，hash为会话摘要，now和idle为有效性边界，update为核验操作。
// 返回值：已提交快照及准确错误，临时故障不写回变更。
func (r *memoryRepo) WithAuthorizationLock(ctx context.Context, hash string, now time.Time,
	idle time.Duration, update func(context.Context, *Session) error) (*Session, error) {
	current, err := r.FindSession(ctx, hash, now, idle)
	if err != nil {
		return nil, err
	}
	err = update(ctx, current)
	if errors.Is(err, ErrUnauthenticated) {
		_ = r.RevokeSession(ctx, hash, now)
		return nil, err
	}
	if err != nil && !errors.Is(err, sharedauthorization.ErrForbidden) {
		return nil, err
	}
	r.sessions[hash] = current
	return current, err
}

// TestFreshCRMFailureNeverUsesStaleAuthorization 验证强制复核故障不使用陈旧权限成功。
// 参数：t为有界旧授权窗口内的真实认证服务测试上下文。
// 返回值：无；未知依赖错误误分类、旧权限成功或故障注销时失败。
func TestFreshCRMFailureNeverUsesStaleAuthorization(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	oidc := &fakeOIDC{exchanged: validClaims(now), current: validClaims(now)}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	login, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(20 * time.Second) }
	for _, failure := range []error{sharedauthorization.ErrUnavailable, context.Canceled} {
		oidc.userInfoErr = failure
		_, err := service.Authenticate(authorizationrefresh.WithForce(context.Background()), login.SessionToken)
		if !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("fresh failure returned %v", err)
		}
		if repo.sessions[tokenHash(login.SessionToken)].RevokedAt != nil {
			t.Fatal("dependency failure revoked session")
		}
	}
}

// TestCRMRevocationClearsProjectionAndCanRecover 验证全撤销拒绝访问且后续授权可沿用Cookie。
// 参数：t为真实认证服务与持久内存会话测试上下文。
// 返回值：无；撤销后旧权限可用、会话被清除或再授权不能恢复时失败。
func TestCRMRevocationClearsProjectionAndCanRecover(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	oidc := &fakeOIDC{exchanged: validClaims(now), current: validClaims(now)}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	login, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	oidc.userInfoErr = sharedauthorization.ErrForbidden
	ctx := authorizationrefresh.WithForce(context.Background())
	if _, err := service.Authenticate(ctx, login.SessionToken); !errors.Is(err, sharedauthorization.ErrForbidden) {
		t.Fatalf("denial=%v", err)
	}
	stored, err := claimsFromSession(repo.sessions[tokenHash(login.SessionToken)])
	if err != nil || len(stored.Roles) != 0 || len(stored.Permissions) != 0 || len(stored.DataScopes) != 0 {
		t.Fatalf("denial retained authorization: %#v %v", stored, err)
	}
	if _, err := service.Authenticate(context.Background(), login.SessionToken); !errors.Is(err, sharedauthorization.ErrForbidden) {
		t.Fatalf("ordinary read after denial=%v", err)
	}
	oidc.userInfoErr = nil
	oidc.current.AuthzRevision = 10
	principal, err := service.Authenticate(ctx, login.SessionToken)
	if err != nil || principal.AuthzRevision != 10 {
		t.Fatalf("same-cookie recovery=%#v %v", principal, err)
	}
}

// TestFreshCRMFailureKeepsCommittedTokenRotation 验证后续授权故障不回滚已消费的单次刷新材料。
// 参数：t为真实认证链与两阶段内存存储测试上下文。
// 返回值：无；刷新材料丢失、重复轮换、原期限变化或故障注销时失败。
func TestFreshCRMFailureKeepsCommittedTokenRotation(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	repo := &centralTestRepository{newMemoryRepo()}
	client := &centralTestClient{state: central.Context{SessionID: "central", TenantID: "tenant-1", SubjectID: "user-1", ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev", PolicyVersion: 1, IdleTimeoutSeconds: 1800, IdleRemainingMS: 1800000, AbsoluteExpiresAt: now.Add(8 * time.Hour), AbsoluteRemainingMS: (8 * time.Hour).Milliseconds()}}
	oidc := &centralTestOIDC{fakeOIDC: fakeOIDC{exchanged: validClaims(now), current: validClaims(now), userInfoErr: sharedauthorization.ErrUnavailable}, refreshedExpiry: now.Add(5 * time.Minute)}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	login, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	row := repo.sessions[tokenHash(login.SessionToken)]
	deadline := row.ExpiresAt
	expiry := now.Add(10 * time.Second)
	row.AccessTokenExpiresAt = &expiry
	row.RefreshTokenCipher, _ = service.codec.encrypt("initial-refresh")
	row.CentralBindingCipher, _ = service.codec.encrypt("binding")
	row.CentralSessionID = "central"
	service.options.CentralClient = client
	service.options.CentralExpectation = central.Expectation{TenantID: "tenant-1", ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}
	ctx := authorizationrefresh.WithForce(context.Background())
	if _, err := service.Authenticate(ctx, login.SessionToken); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("post-rotation authorization failure=%v", err)
	}
	persisted := repo.sessions[tokenHash(login.SessionToken)]
	refresh, err := service.codec.decrypt(persisted.RefreshTokenCipher)
	if err != nil || refresh != "rotated-refresh" || oidc.refreshes != 1 || persisted.RevokedAt != nil || !persisted.ExpiresAt.Equal(deadline) || !persisted.AuthorizationCheckedAt.Equal(time.Unix(1, 0).UTC()) {
		t.Fatal("committed token rotation checkpoint was lost")
	}
	oidc.userInfoErr = nil
	if _, err := service.Authenticate(ctx, login.SessionToken); err != nil || oidc.refreshes != 1 {
		t.Fatalf("same-cookie recovery repeated rotation: error=%v count=%d", err, oidc.refreshes)
	}
}
