package crmauth

import (
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

func TestAuthorizationURLDirectsKeycloakToPlatformBroker(t *testing.T) {
	client := &platformOIDCClient{
		config: oauth2.Config{
			ClientID:    "customer-and-opportunity-prod-web",
			RedirectURL: "http://example.com/customer-opportunity/auth/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL: "http://keycloak.example/realms/basic-platform/protocol/openid-connect/auth",
			},
		},
		identityProviderHint: "basic-platform",
	}

	authorizationURL, err := url.Parse(client.AuthorizationURL("state-value", "nonce-value", "verifier-value"))
	if err != nil {
		t.Fatalf("parse AuthorizationURL(): %v", err)
	}
	query := authorizationURL.Query()
	if got := query.Get("kc_idp_hint"); got != "basic-platform" {
		t.Fatalf("kc_idp_hint = %q, want basic-platform", got)
	}
	if got := query.Get("state"); got != "state-value" {
		t.Fatalf("state = %q, want state-value", got)
	}
	if got := query.Get("nonce"); got != "nonce-value" {
		t.Fatalf("nonce = %q, want nonce-value", got)
	}
	if query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE parameters are incomplete: %s", query.Encode())
	}
}
