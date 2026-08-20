package crmauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

func TestCRMAuthorizationContextCarriesTrustedLoginIP(t *testing.T) {
	permissions := catalogRolePermissions("sales")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub": "user-a", "identity_id": "user-a", "tenant_id": "tenant-a", "client_id": "crm-web", "application_code": "customer_and_opportunity", "environment_code": "dev",
			"person_id": "", "roles": []string{"sales"}, "permissions": permissions,
			"data_scopes": []map[string]string{{"role_code": "sales", "scope_type": "APPLICATION", "scope_id": "", "environment_code": ""}}, "authorization_revision": 1, "user_login_ip": "203.0.113.9",
		})
	}))
	defer server.Close()
	client := &platformOIDCClient{platformBaseURL: server.URL, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}}
	claims, err := client.AuthorizationContext(context.Background(), "access-token")
	if err != nil || claims.LoginIP != "203.0.113.9" {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
}

func TestCRMAuthorizationContextOmitsPrivateLoginIP(t *testing.T) {
	permissions := catalogRolePermissions("sales")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub": "user-a", "identity_id": "user-a", "tenant_id": "tenant-a", "client_id": "crm-web", "application_code": "customer_and_opportunity", "environment_code": "dev",
			"person_id": "", "roles": []string{"sales"}, "permissions": permissions,
			"data_scopes": []map[string]string{{"role_code": "sales", "scope_type": "APPLICATION", "scope_id": "", "environment_code": ""}}, "authorization_revision": 1, "user_login_ip": "172.18.0.2",
		})
	}))
	defer server.Close()
	client := &platformOIDCClient{platformBaseURL: server.URL, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "crm-web", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev"}}
	claims, err := client.AuthorizationContext(context.Background(), "access-token")
	if err != nil || claims.LoginIP != "" {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
}
