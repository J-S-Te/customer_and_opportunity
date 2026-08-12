package portalbootstrap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPortalAuthorizationContextCarriesDataScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/oauth2/authorization-context" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization=%q", got)
		}
		_, _ = io.WriteString(w, `{"sub":"subject-a","identity_id":"subject-a","tenant_id":"tenant-a","roles":["portal_customer"],"permissions":["project.read"],"data_scopes":[{"role_code":"portal_customer","scope_type":"CUSTOMER","scope_id":"customer-7","environment_code":"prod"}],"authorization_revision":7}`)
	}))
	defer server.Close()

	adapter := &OIDCAdapter{platformBaseURL: server.URL, httpClient: server.Client()}
	claims, err := adapter.authorizationContext(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.DataScopes) != 1 || claims.DataScopes[0].ScopeID != "customer-7" || claims.DataScopes[0].EnvironmentCode != "prod" {
		t.Fatalf("data scopes=%#v", claims.DataScopes)
	}
}
