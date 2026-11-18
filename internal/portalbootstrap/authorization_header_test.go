package portalbootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

// TestPortalAuthorizationContextForwardsFreshRequirement 验证门户适配器只转发内部可信刷新要求。
// 参数：t为独立门户HTTP授权服务测试上下文。
// 返回值：无；强制读未绕过平台缓存或应用认证材料丢失时失败。
func TestPortalAuthorizationContextForwardsFreshRequirement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/oauth2/authorization-context" || request.Header.Get("Authorization") != "Bearer access-token" {
			t.Error("portal authorization request lost its binding")
		}
		if request.Header.Get(authorizationrefresh.Header) != "1" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"subject-a","identity_id":"subject-a","tenant_id":"tenant-a","client_id":"portal-web","application_code":"customer_portal","environment_code":"dev","roles":["portal_customer"],"permissions":["project.read"],"data_scopes":[{"role_code":"portal_customer","scope_type":"APPLICATION"}],"authorization_revision":9}`))
	}))
	defer server.Close()
	adapter := &OIDCAdapter{platformBaseURL: server.URL, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "portal-web", ApplicationCode: "customer_portal", EnvironmentCode: "dev"}}
	if _, err := adapter.authorizationContext(context.Background(), "access-token"); !errors.Is(err, sharedauthorization.ErrForbidden) {
		t.Fatalf("ordinary request=%v", err)
	}
	current, err := adapter.authorizationContext(authorizationrefresh.WithForce(context.Background()), "access-token")
	if err != nil || current.AuthzRevision != 9 {
		t.Fatalf("forced request=%#v %v", current, err)
	}
}
