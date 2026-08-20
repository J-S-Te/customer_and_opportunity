package portalbootstrap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"golang.org/x/oauth2"
)

func TestPortalAuthorizationContextCarriesDataScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/oauth2/authorization-context" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization=%q", got)
		}
		_, _ = io.WriteString(w, `{"sub":"subject-a","identity_id":"subject-a","tenant_id":"tenant-a","client_id":"portal-prod-web","application_code":"customer_portal","environment_code":"prod","roles":["portal_customer"],"permissions":["project.read"],"data_scopes":[{"role_code":"portal_customer","scope_type":"ENVIRONMENT","scope_id":"01ENV","environment_code":"prod"}],"authorization_revision":7,"user_login_ip":"203.0.113.9"}`)
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
	if claims.LoginIP != "203.0.113.9" {
		t.Fatalf("login IP=%q", claims.LoginIP)
	}
}

func TestPortalAuthorizationContextOmitsInvalidLoginIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"sub":"subject-a","identity_id":"subject-a","tenant_id":"tenant-a","client_id":"portal-prod-web","application_code":"customer_portal","environment_code":"prod","roles":["portal_customer"],"permissions":["project.read"],"data_scopes":[{"role_code":"portal_customer","scope_type":"APPLICATION","scope_id":"","environment_code":""}],"authorization_revision":7,"user_login_ip":"172.18.0.2"}`)
	}))
	defer server.Close()
	adapter := &OIDCAdapter{platformBaseURL: server.URL, httpClient: server.Client(), expectedContext: sharedauthorization.Expectation{ClientID: "portal-prod-web", ApplicationCode: "customer_portal", EnvironmentCode: "prod"}}
	claims, err := adapter.authorizationContext(context.Background(), "access-token")
	if err != nil || claims.LoginIP != "" {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
}

func TestCompactPortalIdentityRequiresIDTokenPurposeAndCanonicalIdentity(t *testing.T) {
	base := compactOIDCClaims{Subject: "keycloak-subject-a", IdentityID: "platform-identity-a", Nonce: "nonce", TokenUse: "id_token"}
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

func TestCompactPortalClaimsPreservesStableIdentityWhenSubjectDiffers(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute).UTC()
	claims := compactPortalClaims(compactOIDCClaims{
		Subject:    "keycloak-subject-a",
		IdentityID: "platform-identity-a",
		TenantID:   "tenant-a",
	}, "sha256:catalog", expiresAt, "access-token")

	if claims.Subject != "keycloak-subject-a" || claims.IdentityID != "platform-identity-a" {
		t.Fatalf("identity mapping = subject %q, identity_id %q", claims.Subject, claims.IdentityID)
	}
	if claims.TenantID != "tenant-a" || claims.RoleConfigHash != "sha256:catalog" || !claims.ExpiresAt.Equal(expiresAt) || claims.AccessToken != "access-token" {
		t.Fatalf("claims = %#v", claims)
	}
}

func TestPortalAuthorizationURLCarriesBrokerHintAndPKCE(t *testing.T) {
	adapter := &OIDCAdapter{
		config: oauth2.Config{
			ClientID: "customer_portal-prod-web",
			Endpoint: oauth2.Endpoint{AuthURL: "https://sso.example/realms/basic-platform/protocol/openid-connect/auth"},
		},
		idpHint: "basic-platform",
	}
	target, err := adapter.AuthorizationURL("state-a", "nonce-a", "challenge-a", "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"state":                 "state-a",
		"nonce":                 "nonce-a",
		"code_challenge":        "challenge-a",
		"code_challenge_method": "S256",
		"kc_idp_hint":           "basic-platform",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q; URL=%s", key, got, want, target)
		}
	}
}
