package portalbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/capability"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type refreshRouteRepository struct {
	*reportNotificationRouteAccountRepository
}
type refreshRouteOIDC struct{ failure error }
type refreshRouteProtector struct{}
type unavailableCapabilityReader struct{}

// WithAuthorizationLock 模拟独立授权事务，保留生产认证器产生的新快照。
// 参数：ctx为请求，其余标识限定当前会话，update执行真实认证服务的核验。
// 返回值：提交后的独立副本，依赖错误不写回。
func (r *refreshRouteRepository) WithAuthorizationLock(ctx context.Context, _, _ string, _ time.Time,
	update func(context.Context, *account.Session) error) (*account.Session, error) {
	current := *r.session
	err := update(ctx, &current)
	if err != nil && !errors.Is(err, account.ErrPortalAuthorization) {
		return nil, err
	}
	r.session = &current
	returned := current
	return &returned, err
}

// MarkLinkVerified 接受已经核对的测试映射，不替换真实客户校验。
// 参数：请求及映射版本材料来自真实门户认证服务。
// 返回值：无错误。
func (*refreshRouteRepository) MarkLinkVerified(context.Context, string, uint64, uint64, time.Time) error {
	return nil
}

// AuthorizationURL 禁止本测试意外进入登录流程。
// 参数：认证事务材料不使用。
// 返回值：未使用错误。
func (*refreshRouteOIDC) AuthorizationURL(string, string, string, string) (string, error) {
	return "", errors.New("unused login")
}

// ExchangeAndValidate 禁止用重新登录掩盖会话刷新。
// 参数：登录材料不使用。
// 返回值：未使用错误。
func (*refreshRouteOIDC) ExchangeAndValidate(context.Context, string, string, string) (account.Claims, error) {
	return account.Claims{}, errors.New("unused exchange")
}

// UserInfo 为真实门户认证器提供当前有效授权。
// 参数：ctx携带内部刷新要求，token是服务器端解密的访问材料。
// 返回值：合法新授权或指定平台依赖错误。
func (o *refreshRouteOIDC) UserInfo(ctx context.Context, token string) (account.Claims, error) {
	if !authorizationrefresh.Forced(ctx) || token != "access-token" {
		return account.Claims{}, errors.New("fresh context or token was lost")
	}
	return account.Claims{Subject: "subject-a", IdentityID: "subject-a", TenantID: "tenant-a", RoleConfigHash: "catalog", AuthzRevision: 9,
		Roles: []string{"portal_customer"}, Permissions: []string{"project.read", "report.read"},
		DataScopes: []account.DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}}, o.failure
}

// Encrypt 为本地测试保留密文存储边界，不建立外部依赖。
// 参数：原请求与待保存的测试访问材料。
// 返回值：独立字节副本。
func (refreshRouteProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

// Decrypt 为真实认证服务还原测试访问材料。
// 参数：原请求与内存会话中的密文字节。
// 返回值：独立材料副本。
func (refreshRouteProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

// Get 模拟客户能力读取暂时不可用。
// 参数：请求和客户边界已由真实门户认证器验证。
// 返回值：空能力与依赖错误，不允许伪造可用能力。
func (unavailableCapabilityReader) Get(context.Context, string, uint64) (capability.Options, error) {
	return capability.Options{}, context.DeadlineExceeded
}

// freshPortalRouter 建立窗口内已有会话和真实门户路由。
// 参数：t为测试上下文，failure为平台错误，reader为当前客户商业能力来源。
// 返回值：真实路由与原会话存储。
func freshPortalRouter(t *testing.T, failure error, reader capability.Reader) (*gin.Engine, *refreshRouteRepository) {
	t.Helper()
	now := time.Now().UTC()
	repo := &refreshRouteRepository{&reportNotificationRouteAccountRepository{
		session: &account.Session{Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: reportNotificationRouteSessionHash("opaque-session"),
			PlatformUserID: "subject-a", CustomerID: 7, Roles: []string{"portal_customer"}, Permissions: []string{"project.read", "report.read", "feedback.read"},
			DataScopes: []account.DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}, AccessTokenCipher: []byte("access-token"),
			AuthzRevision: 8, RoleConfigHash: "catalog", AuthorizationCheckedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiry: now.Add(time.Hour), LastSeenAt: now.Add(-time.Minute)},
		link: &account.IdentityLink{Model: database.Model{TenantID: "tenant-a"}, PlatformUserID: "subject-a", CustomerID: 7, Status: account.IdentityActive},
	}}
	service := account.NewService(repo, &refreshRouteOIDC{failure: failure}, nil, refreshRouteProtector{}, reportNotificationRouteClock{now: now}, nil, "catalog", time.Hour)
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	return NewRouter(RouterDependencies{Config: config, Account: service, CustomerCapabilities: reader}), repo
}

// TestPortalFreshHTTPReturnsCurrentAuthorizationAndCapabilities 验证原Cookie刷新同时重算授权和商业能力交集。
// 参数：t为真实门户路由、认证器与内存存储测试上下文。
// 返回值：无；响应残留旧权限、能力未收窄或Cookie被清除时失败。
func TestPortalFreshHTTPReturnsCurrentAuthorizationAndCapabilities(t *testing.T) {
	options := capability.DefaultOptions()
	options.ReportEnabled = false
	router, repo := freshPortalRouter(t, nil, &capabilityStub{options: options})
	originalSeen := repo.session.LastSeenAt
	request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/auth/me", nil)
	request.Header.Set(authorizationrefresh.Header, "1")
	request.Header.Set("X-Roles", "portal_super_admin")
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Revision    uint64   `json:"authz_revision"`
			Permissions []string `json:"permissions"`
			CustomerID  uint64   `json:"customer_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Revision != 9 || len(envelope.Data.Permissions) != 1 || envelope.Data.Permissions[0] != "project.read" || envelope.Data.CustomerID != 7 {
		t.Fatalf("authorization/capability projection=%#v", envelope.Data)
	}
	if len(response.Result().Cookies()) != 0 || !repo.session.LastSeenAt.Equal(originalSeen) {
		t.Fatal("fresh read changed the login or idle deadline")
	}
}

// TestPortalFreshHTTPSeparatesAuthenticationAuthorizationAndDependency 验证真实入口401、403、503互不混淆。
// 参数：t为平台撤销、认证拒绝、依赖及客户能力故障测试上下文。
// 返回值：无；错误状态错误或故障清理Cookie时失败。
func TestPortalFreshHTTPSeparatesAuthenticationAuthorizationAndDependency(t *testing.T) {
	for _, item := range []struct {
		name    string
		failure error
		reader  capability.Reader
		status  int
	}{
		{"authorization", sharedauthorization.ErrForbidden, nil, 403},
		{"authentication", sharedauthorization.ErrTokenRejected, nil, 401},
		{"dependency", sharedauthorization.ErrUnavailable, nil, 503},
		{"capability", nil, unavailableCapabilityReader{}, 503},
	} {
		t.Run(item.name, func(t *testing.T) {
			router, _ := freshPortalRouter(t, item.failure, item.reader)
			request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/auth/me", nil)
			request.Header.Set(authorizationrefresh.Header, "1")
			request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != item.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, item.status, response.Body.String())
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("refresh failure cleared browser login")
			}
		})
	}
}
