package commercial

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/J-S-Te/license-core"
	runtime "github.com/J-S-Te/license-core/runtime"
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// 联合验证生产 HTTP 策略与持久化、独立签名的运行时
// 状态。主体来自已认证上下文测试夹具；本测试
// 不覆盖 OIDC、数据库处理器或平台同步。
func TestSignedMigrationSnapshotExpiryRejectsBusinessForAllRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type route struct {
		method, template, request, permission string
		operation                             core.Operation
	}
	cases := []struct {
		kind, application, prefix string
		routes                    []route
	}{
		{"crm", "customer_and_opportunity", "/customer-opportunity", []route{
			{http.MethodPost, "/api/v1/customers", "/api/v1/customers", "customer.create", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/opportunities", "/api/v1/opportunities", "opportunity.create", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/opportunities/:id/launch/quotation", "/api/v1/opportunities/existing/launch/quotation", "opportunity.update", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/opportunities/:id/launch/bid", "/api/v1/opportunities/existing/launch/bid", "opportunity.update", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/presale/requests", "/api/v1/presale/requests", "presale.create", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/presale/requests/:id/approval-actions", "/api/v1/presale/requests/existing/approval-actions", "presale.approve", core.MUTATE_BUSINESS},
			{http.MethodGet, "/api/v1/customers", "/api/v1/customers", "customer.read", core.READ_HISTORY},
			{http.MethodPost, "/api/v1/customer-exports", "/api/v1/customer-exports", "customer.export", core.EXPORT_HISTORY},
			{http.MethodPost, "/api/v1/notifications/:id/read", "/api/v1/notifications/existing/read", "notification.read", core.ESSENTIAL_SERVICE},
		}},
		{"portal", "customer_portal", "/customer-portal", []route{
			{http.MethodPost, "/api/v1/reports", "/api/v1/reports", "report.request", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/filings", "/api/v1/filings", "filing.create", core.MUTATE_BUSINESS},
			{http.MethodPost, "/api/v1/filings/:id/submit", "/api/v1/filings/existing/submit", "filing.submit", core.MUTATE_BUSINESS},
			{http.MethodGet, "/api/v1/filings", "/api/v1/filings", "filing.read", core.READ_HISTORY},
			{http.MethodPost, "/api/v1/filings/:id/exports", "/api/v1/filings/existing/exports", "filing.read", core.EXPORT_HISTORY},
			{http.MethodDelete, "/api/v1/account/sessions/:id", "/api/v1/account/sessions/existing", "account.security", core.ESSENTIAL_SERVICE},
		}},
	}
	for _, tc := range cases {
		for _, role := range []struct {
			name  string
			roles []string
			scope auth.ScopeMode
		}{
			{"super_administrator", []string{"crm_super_admin", "portal_super_admin"}, auth.ScopeAll},
			{"ordinary_user_with_business_permission", []string{"salesperson"}, auth.ScopeSelf},
		} {
			t.Run(tc.kind+"/"+role.name, func(t *testing.T) {
				platformPublic, platformPrivate, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				vendorPublic, vendorPrivate, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Unix(1800000000, 0).UTC()
				expires := now.Add(time.Minute)
				binding := runtime.Binding{InstanceID: "expiry-test-instance", Environment: "production", Application: tc.application, ServiceID: tc.kind + "-api"}
				statePath := filepath.Join(t.TempDir(), "state.json")
				platformKeys := map[string]ed25519.PublicKey{"platform-test": platformPublic}
				vendorKeys := map[string]ed25519.PublicKey{"vendor-test": vendorPublic}
				reopen := func() *runtime.FileRuntime {
					t.Helper()
					rt, err := runtime.NewFileRuntime(statePath, binding, platformKeys, vendorKeys)
					if err != nil {
						t.Fatal(err)
					}
					return rt
				}
				rt := reopen()
				license, err := core.Sign(core.License{
					ProtocolVersion: core.ProtocolVersion, Issuer: core.Issuer, ID: "expiry-test-license", Version: 1,
					CustomerID: "test-customer", ProductID: core.Product, Environment: binding.Environment, InstanceID: binding.InstanceID,
					IssuedAt: now.Unix() - 1000, NotBefore: now.Unix() - 100,
					Applications: []core.Application{{Code: tc.application, NotBefore: now.Unix() - 100, ExpiresAt: expires.Unix(), Kind: "FULL"}},
				}, "vendor-test", vendorPrivate)
				if err != nil {
					t.Fatal(err)
				}
				signed, err := runtime.SignSnapshot(runtime.PlatformSnapshot{
					Protocol: runtime.Protocol, InstanceID: binding.InstanceID, Environment: binding.Environment,
					Application: binding.Application, ServiceID: binding.ServiceID, Revision: 1,
					EnforcementState: runtime.Pending, MigrationEligible: true,
					CurrentLicenseJWS: license, HighestLicenseVersion: 1, SnapshotIssuedAt: now.Unix(),
				}, "platform-test", platformPrivate)
				if err != nil {
					t.Fatal(err)
				}
				if err := rt.ApplySnapshot(context.Background(), signed, now); err != nil {
					t.Fatal(err)
				}
				permissions := make(map[string]struct{})
				for _, route := range tc.routes {
					permissions[route.permission] = struct{}{}
					if Operation(tc.kind, route.method, route.template) != route.operation {
						t.Fatalf("incorrect route classification: %s %s", route.method, route.template)
					}
				}
				principal := auth.Principal{UserID: role.name, TenantID: "test-tenant", Roles: role.roles, ScopeMode: role.scope, Permissions: permissions}
				handlerCalls := make(map[string]int)
				router := gin.New()
				base := router.Group(tc.prefix)
				base.Use(Middleware(func(ctx context.Context, op core.Operation) error { return rt.Evaluate(ctx, op, now) }, tc.kind, tc.prefix))
				for _, route := range tc.routes {
					key := route.method + " " + route.template
					base.Handle(route.method, route.template, middleware.RequirePermission(route.permission), func(c *gin.Context) {
						if got, ok := auth.FromContext(c.Request.Context()); !ok || got.UserID != role.name {
							t.Error("handler did not receive authenticated Principal")
						}
						handlerCalls[key]++
						c.Status(http.StatusOK)
					})
				}
				// 不连接同步客户端或服务端；仅凭本地签名状态
				// 也必须约束已流逝的绝对时间，包括进程重建之后。
				for _, stage := range []string{"valid", "expiry_boundary", "offline_after_expiry", "reopened_after_expiry"} {
					switch stage {
					case "expiry_boundary":
						now = expires
					case "offline_after_expiry":
						now = expires.Add(time.Hour)
					case "reopened_after_expiry":
						rt = reopen()
					}
					for _, route := range tc.routes {
						key := route.method + " " + route.template
						before := handlerCalls[key]
						request := httptest.NewRequest(route.method, tc.prefix+route.request, nil)
						request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
						response := httptest.NewRecorder()
						router.ServeHTTP(response, request)
						denied := stage != "valid" && route.operation == core.MUTATE_BUSINESS
						if denied {
							if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "COMMERCIAL_LICENSE_DENIED") || handlerCalls[key] != before {
								t.Errorf("%s %s: expired mutation reached handler or wrong response: status=%d calls=%d before=%d body=%s", stage, key, response.Code, handlerCalls[key], before, response.Body.String())
							}
						} else if response.Code != http.StatusOK || handlerCalls[key] != before+1 {
							t.Errorf("%s %s: valid/history/export/essential operation rejected: status=%d", stage, key, response.Code)
						}
					}
				}
				// 保留历史权限不得移除已有的业务
				// 权限检查，即使角色声明具有特权。
				principal.Permissions = nil
				for _, route := range tc.routes {
					if route.operation != core.READ_HISTORY && route.operation != core.EXPORT_HISTORY {
						continue
					}
					key := route.method + " " + route.template
					before := handlerCalls[key]
					request := httptest.NewRequest(route.method, tc.prefix+route.request, nil)
					request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					if response.Code != http.StatusForbidden || handlerCalls[key] != before {
						t.Errorf("history/export bypassed business permission: %s", key)
					}
				}
			})
		}
	}
}
