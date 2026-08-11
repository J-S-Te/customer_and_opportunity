package crmauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlatformBrokerVerifierPostsServerSideBoundVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/keycloak/broker-login-verifications" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer server-only-access-token" {
			t.Fatal("broker request did not use the server-held OIDC access token")
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"application_code": "customer_and_opportunity", "environment": "test", "identity_id": "user-1", "issuer": "https://sso.example/realms/main", "client_id": "crm-web"}
		for key, value := range want {
			if payload[key] != value {
				t.Fatalf("payload[%q] = %q, want %q", key, payload[key], value)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":"OK"}`))
	}))
	defer server.Close()
	verifier, err := NewPlatformBrokerVerifier(BrokerVerificationOptions{PlatformBaseURL: server.URL, ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test", Issuer: "https://sso.example/realms/main/", ClientID: "crm-web"})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifier.Verify(context.Background(), verifiedClaims{IdentityID: "user-1", Subject: "user-1", AccessToken: "server-only-access-token"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformBrokerVerifierRejectsPlatformFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	verifier, err := NewPlatformBrokerVerifier(BrokerVerificationOptions{PlatformBaseURL: server.URL, ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test", Issuer: "https://sso.example", ClientID: "crm-web"})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifier.Verify(context.Background(), verifiedClaims{IdentityID: "user-1", Subject: "user-1", AccessToken: "server-only-access-token"}); err == nil {
		t.Fatal("platform rejection unexpectedly succeeded")
	}
}
