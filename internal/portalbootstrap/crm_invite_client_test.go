package portalbootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCRMInviteClientUsesClientCredentialsAndReplayHeaders(t *testing.T) {
	const clientID = "portal-crm-invite"
	const clientSecret = "do-not-leak-client-secret"
	const accessToken = "do-not-leak-access-token"
	now := time.Date(2026, 7, 31, 10, 11, 12, 345678900, time.UTC)
	var mu sync.Mutex
	var tokenCalls int
	var nonces []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			tokenCalls++
			if gotID, gotSecret, ok := request.BasicAuth(); !ok || gotID != clientID || gotSecret != clientSecret {
				t.Errorf("unexpected client authentication: id=%q secret=%q ok=%t", gotID, gotSecret, ok)
			}
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != "portal.invite.verify" {
				t.Errorf("unexpected token form: %v", request.Form)
			}
			if _, sent := request.Form["audience"]; sent {
				t.Errorf("platform token endpoint rejects non-standard audience: %v", request.Form)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 300, "scope": "portal.invite.verify"})
		case "/internal/portal/invites/verify":
			if request.Header.Get("Authorization") != "Bearer "+accessToken {
				t.Errorf("unexpected bearer authorization")
			}
			if request.Header.Get("X-Integration-Timestamp") != now.Format(time.RFC3339Nano) {
				t.Errorf("timestamp=%q", request.Header.Get("X-Integration-Timestamp"))
			}
			nonce := request.Header.Get("X-Integration-Nonce")
			decoded, err := base64.RawURLEncoding.DecodeString(nonce)
			if err != nil || len(decoded) != 32 {
				t.Errorf("nonce is not 256-bit URL-safe random data: %q", nonce)
			}
			mu.Lock()
			nonces = append(nonces, nonce)
			mu.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"code": "OK", "message": "success", "request_id": "crm-request", "data": map[string]any{"tenant_id": "tenant-a", "platform_user_id": "subject-a", "customer_id": 7}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewCRMInviteClient(context.Background(), CRMInviteClientOptions{
		BaseURL: server.URL, TokenURL: server.URL + "/oauth2/token", ClientID: clientID,
		ClientSecret: clientSecret, Scope: "portal.invite.verify", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		invite, verifyErr := client.Verify(context.Background(), "one-time-token")
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		if invite.TenantID != "tenant-a" || invite.ExpectedPlatformUserID != "subject-a" || invite.CustomerID != 7 {
			t.Fatalf("unexpected envelope data: %#v", invite)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("access token was not cached: token calls=%d", tokenCalls)
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("request nonce was reused: %v", nonces)
	}
}

func TestCRMInviteClientRejectsWrongMachineContract(t *testing.T) {
	base := CRMInviteClientOptions{BaseURL: "https://crm.example", TokenURL: "https://identity.example/oauth2/token", ClientID: "id", ClientSecret: "secret", Scope: "portal.invite.verify"}
	for name, mutate := range map[string]func(*CRMInviteClientOptions){
		"scope": func(value *CRMInviteClientOptions) { value.Scope = "customer.summary.read" },
	} {
		t.Run(name, func(t *testing.T) {
			options := base
			mutate(&options)
			if _, err := NewCRMInviteClient(context.Background(), options); err == nil {
				t.Fatal("expected strict contract validation failure")
			}
		})
	}
}

func TestCRMInviteClientErrorsNeverLeakCredentialsOrBodies(t *testing.T) {
	const secret = "super-secret-client-credential"
	const responseBody = "upstream-private-diagnostics"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "private-access-token", "token_type": "Bearer", "expires_in": 300})
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(responseBody))
	}))
	defer server.Close()
	client, err := NewCRMInviteClient(context.Background(), CRMInviteClientOptions{BaseURL: server.URL, TokenURL: server.URL + "/oauth2/token", ClientID: "client", ClientSecret: secret, Scope: "portal.invite.verify"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Verify(context.Background(), "private-invite-token")
	if err == nil {
		t.Fatal("expected upstream error")
	}
	for _, forbidden := range []string{secret, responseBody, "private-access-token", "private-invite-token"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

func TestCRMInviteTransportErrorIsSanitized(t *testing.T) {
	secretURL := "https://invalid.example/path?secret=leaked"
	client, err := NewCRMInviteClient(context.Background(), CRMInviteClientOptions{
		BaseURL: "https://crm.example", TokenURL: "https://identity.example/oauth2/token", ClientID: "client", ClientSecret: "secret",
		Scope: "portal.invite.verify", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: secretURL, Err: errors.New("sensitive transport detail")}
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Verify(context.Background(), "token")
	if err == nil || strings.Contains(err.Error(), secretURL) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
