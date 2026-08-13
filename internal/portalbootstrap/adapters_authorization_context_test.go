package portalbootstrap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

func TestPortalAuthorizationContextCarriesDataScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/oauth2/authorization-context" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization=%q", got)
		}
		_, _ = io.WriteString(w, `{"sub":"subject-a","identity_id":"subject-a","tenant_id":"tenant-a","client_id":"portal-prod-web","application_code":"customer_portal","environment_code":"prod","roles":["portal_customer"],"permissions":["project.read"],"data_scopes":[{"role_code":"portal_customer","scope_type":"ENVIRONMENT","scope_id":"01ENV","environment_code":"prod"}],"authorization_revision":7}`)
	}))
	defer server.Close()

	adapter := &OIDCAdapter{platformBaseURL: server.URL, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "portal-prod-web", ApplicationCode: "customer_portal", EnvironmentCode: "prod"}}
	claims, err := adapter.authorizationContext(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.DataScopes) != 1 || claims.DataScopes[0].ScopeID != "01ENV" || claims.DataScopes[0].EnvironmentCode != "prod" {
		t.Fatalf("data scopes=%#v", claims.DataScopes)
	}
}

func TestCompactPortalIdentityRequiresIDTokenPurposeAndCanonicalIdentity(t *testing.T) {
	base := compactOIDCClaims{Subject: "identity-a", IdentityID: "identity-a", Nonce: "nonce", TokenUse: "id_token"}
	if !validCompactPortalIdentity(base, "nonce", "access-token") {
		t.Fatal("valid compact identity was rejected")
	}
	for _, mutate := range []func(*compactOIDCClaims){
		func(value *compactOIDCClaims) { value.TokenUse = "" },
		func(value *compactOIDCClaims) { value.TokenUse = "access_token" },
		func(value *compactOIDCClaims) { value.IdentityID = "" },
		func(value *compactOIDCClaims) { value.Subject = " identity-a" },
	} {
		value := base
		mutate(&value)
		if validCompactPortalIdentity(value, "nonce", "access-token") {
			t.Fatalf("invalid identity accepted: %#v", value)
		}
	}
}
