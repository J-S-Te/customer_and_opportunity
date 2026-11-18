package crmauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

// TestAuthorizationContextForwardsFreshRequirement 验证在线适配器保留认证材料并向平台传递刷新标记。
// 参数：t为本地HTTP授权服务测试上下文。
// 返回值：无；普通请求误加标记、强制读丢标记或错误认证头时失败。
func TestAuthorizationContextForwardsFreshRequirement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/oauth2/authorization-context" || request.Header.Get("Authorization") != "Bearer access-token" {
			t.Error("authorization request lost its trusted binding")
		}
		if request.Header.Get(authorizationrefresh.Header) != "1" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"subject-a","identity_id":"subject-a","tenant_id":"tenant-a","client_id":"crm-web","application_code":"customer_and_opportunity","environment_code":"dev","roles":["sales"],"permissions":["customer.read"],"data_scopes":[{"role_code":"sales","scope_type":"APPLICATION"}],"authorization_revision":9}`))
	}))
	defer server.Close()
	client := &platformOIDCClient{platformBaseURL: server.URL, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}}
	if _, err := client.AuthorizationContext(context.Background(), "access-token"); !errors.Is(err, sharedauthorization.ErrForbidden) {
		t.Fatalf("ordinary request=%v", err)
	}
	current, err := client.AuthorizationContext(authorizationrefresh.WithForce(context.Background()), "access-token")
	if err != nil || current.AuthzRevision != 9 {
		t.Fatalf("forced request=%#v %v", current, err)
	}
}
