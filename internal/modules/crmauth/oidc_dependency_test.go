package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

// TestCRMUserInfoOutageIsUnavailable 验证真实OIDC适配器的资料端点故障不会导致会话被撤销。
// 参数：t为临时HTTP提供方和有效授权快照的适配器测试上下文。
// 返回值：无；资料端点503被误分类为未认证即失败。
func TestCRMUserInfoOutageIsUnavailable(t *testing.T) {
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": endpoint, "authorization_endpoint": endpoint + "/authorize", "token_endpoint": endpoint + "/token", "jwks_uri": endpoint + "/keys", "userinfo_endpoint": endpoint + "/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/userinfo":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-a", "identity_id": "user-a", "tenant_id": "tenant-a", "client_id": "crm-web", "application_code": "customer_and_opportunity", "environment_code": "dev", "roles": []string{"sales"}, "permissions": catalogRolePermissions("sales"), "data_scopes": []map[string]string{{"role_code": "sales", "scope_type": "APPLICATION", "scope_id": "", "environment_code": ""}}, "authorization_revision": 1})
		}
	}))
	defer server.Close()
	endpoint = server.URL
	provider, err := oidc.NewProvider(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := &platformOIDCClient{central: &centralTestClient{}, provider: provider, platformBaseURL: endpoint, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}}
	if _, err := client.UserInfo(context.Background(), "access-token"); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("UserInfo outage lost dependency semantics: %v", err)
	}
}

// TestCRMAuthorizationTransportFailureIsUnavailable 验证原请求取消不会被当成令牌拒绝。
// 参数：t为不发出实际网络请求的适配器测试上下文。
// 返回值：无；取消丢失依赖语义即失败。
func TestCRMAuthorizationTransportFailureIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &platformOIDCClient{central: &centralTestClient{}, platformBaseURL: "http://127.0.0.1:1", httpClient: &http.Client{}}
	if _, err := client.AuthorizationContext(ctx, "access-token"); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("cancelled request misclassified: %v", err)
	}
}
