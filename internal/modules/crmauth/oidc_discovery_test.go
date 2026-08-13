package crmauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPlatformOIDCClientUsesDiscoveredEndSessionEndpoint(t *testing.T) {
	server := newOIDCDiscoveryServer(t, func(issuer string) map[string]any {
		return map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
			"end_session_endpoint":   issuer + "/realms/crm/protocol/openid-connect/logout",
		}
	})
	defer server.Close()

	client, err := NewPlatformOIDCClient(context.Background(), OIDCOptions{Issuer: server.URL, ClientID: "crm-web"})
	if err != nil {
		t.Fatalf("NewPlatformOIDCClient() error = %v", err)
	}
	want := server.URL + "/realms/crm/protocol/openid-connect/logout"
	if got := client.EndSessionEndpoint(); got != want {
		t.Fatalf("EndSessionEndpoint() = %q, want %q", got, want)
	}
}

func TestNewPlatformOIDCClientKeepsLegacyLogoutFallback(t *testing.T) {
	server := newOIDCDiscoveryServer(t, func(issuer string) map[string]any {
		return map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
		}
	})
	defer server.Close()

	client, err := NewPlatformOIDCClient(context.Background(), OIDCOptions{Issuer: server.URL, ClientID: "crm-web"})
	if err != nil {
		t.Fatalf("NewPlatformOIDCClient() error = %v", err)
	}
	want := server.URL + "/oauth2/logout"
	if got := client.EndSessionEndpoint(); got != want {
		t.Fatalf("EndSessionEndpoint() = %q, want %q", got, want)
	}
}

func TestNewPlatformOIDCClientRejectsUnsafeDiscoveredLogoutEndpoint(t *testing.T) {
	server := newOIDCDiscoveryServer(t, func(issuer string) map[string]any {
		return map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
			"end_session_endpoint":   "javascript:alert(1)",
		}
	})
	defer server.Close()

	if _, err := NewPlatformOIDCClient(context.Background(), OIDCOptions{Issuer: server.URL, ClientID: "crm-web"}); err == nil {
		t.Fatal("NewPlatformOIDCClient() accepted an unsafe end_session_endpoint")
	}
}

func newOIDCDiscoveryServer(t *testing.T, metadata func(string) map[string]any) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(metadata(server.URL)); err != nil {
			t.Errorf("encode discovery metadata: %v", err)
		}
	}))
	return server
}
