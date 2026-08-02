package portalinvite

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

func TestHTTPPlatformProvisionerUsesIsolatedExactScopesAndStrictContracts(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	scopes := make(map[string]string)
	var provisionKey, roleKey, provisionNonce, roleNonce string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			clientID, _, ok := request.BasicAuth()
			if !ok || request.FormValue("grant_type") != "client_credentials" || request.FormValue("client_id") != "" || request.FormValue("client_secret") != "" {
				http.Error(writer, "bad token request", http.StatusBadRequest)
				return
			}
			mutex.Lock()
			scopes[clientID] = request.FormValue("scope")
			mutex.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": clientID + "-token", "token_type": "Bearer", "expires_in": 300})
		case "/external-users":
			if request.Header.Get("Authorization") != "Bearer provision-client-token" || request.Header.Get("X-Tenant-ID") != "" || request.Header.Get("X-Request-ID") != "trace-1" {
				http.Error(writer, "bad provision auth", http.StatusUnauthorized)
				return
			}
			provisionKey, provisionNonce = request.Header.Get("Idempotency-Key"), request.Header.Get("X-Integration-Nonce")
			body, _ := io.ReadAll(request.Body)
			if strings.Contains(string(body), "tenant-a") || !strings.Contains(string(body), `"display_name":"Customer User"`) {
				http.Error(writer, "bad provision body", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-1","data":{"platform_user_id":"subject-1","account_no":"EXT-1"}}`))
		case "/application-roles":
			if request.Header.Get("Authorization") != "Bearer role-client-token" || request.Header.Get("X-Tenant-ID") != "" {
				http.Error(writer, "bad role auth", http.StatusUnauthorized)
				return
			}
			roleKey, roleNonce = request.Header.Get("Idempotency-Key"), request.Header.Get("X-Integration-Nonce")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-2","data":{"platform_user_id":"subject-1","application_code":"customer_portal","role_code":"portal_customer","status":"ACTIVE"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewHTTPPlatformProvisioner(context.Background(), PlatformProvisionerOptions{
		ProvisionURL: server.URL + "/external-users", RoleAssignURL: server.URL + "/application-roles",
		TokenURL: server.URL + "/oauth2/token", ApplicationCode: "customer_portal",
		ProvisionClientID: "provision-client", ProvisionClientSecret: "secret", ProvisionScope: externalUserProvisionScope,
		RoleClientID: "role-client", RoleClientSecret: "secret", RoleScope: applicationRoleAssignScope,
		HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) },
		NonceReader: strings.NewReader(strings.Repeat("n", 32) + strings.Repeat("r", 96)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestctx.WithID(context.Background(), "trace-1")
	identity, err := client.ProvisionExternalUser(ctx, ContactIdentity{TenantID: "tenant-a", CustomerID: 9, ContactID: 7, DisplayName: "Customer User", Phone: "13800000000"})
	if err != nil || identity.PlatformUserID != "subject-1" || identity.AccountNo != "EXT-1" {
		t.Fatalf("identity=%+v error=%v", identity, err)
	}
	if err = client.AssignPortalRole(ctx, identity.PlatformUserID); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if scopes["provision-client"] != externalUserProvisionScope || scopes["role-client"] != applicationRoleAssignScope || provisionKey == "" || roleKey == "" || provisionKey == roleKey || provisionNonce == "" || roleNonce == "" || provisionNonce == roleNonce {
		t.Fatalf("scopes=%v keys=%q/%q nonces=%q/%q", scopes, provisionKey, roleKey, provisionNonce, roleNonce)
	}
}

func TestHTTPPlatformProvisionerRejectsUnknownResponseFieldsAndOverScope(t *testing.T) {
	t.Parallel()
	if _, err := NewHTTPPlatformProvisioner(context.Background(), PlatformProvisionerOptions{
		ProvisionURL: "https://identity.example/external-users", RoleAssignURL: "https://identity.example/application-roles",
		TokenURL: "https://identity.example/oauth2/token", ApplicationCode: "customer_portal",
		ProvisionClientID: "provision", ProvisionClientSecret: "secret", ProvisionScope: "external_user.provision application_role.assign",
		RoleClientID: "role", RoleClientSecret: "secret", RoleScope: applicationRoleAssignScope,
	}); err == nil {
		t.Fatal("over-scoped client configuration was accepted")
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300})
			return
		}
		_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-1","data":{"platform_user_id":"subject-1","account_no":"EXT-1","unexpected":true}}`))
	}))
	defer server.Close()
	client, err := NewHTTPPlatformProvisioner(context.Background(), PlatformProvisionerOptions{
		ProvisionURL: server.URL + "/external-users", RoleAssignURL: server.URL + "/roles", TokenURL: server.URL + "/oauth2/token", ApplicationCode: "customer_portal",
		ProvisionClientID: "provision", ProvisionClientSecret: "secret", ProvisionScope: externalUserProvisionScope,
		RoleClientID: "role", RoleClientSecret: "secret", RoleScope: applicationRoleAssignScope, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ProvisionExternalUser(context.Background(), ContactIdentity{TenantID: "tenant-a", CustomerID: 9, ContactID: 7, DisplayName: "User", Email: "u@example.com"}); err == nil {
		t.Fatal("unknown response field was accepted")
	}
}

func TestHTTPPlatformRoleRevokerUsesDedicatedScopeStableIdempotencyAndDisabledResponse(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	var tokenScope string
	var requestKeys, requestNonces []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			clientID, _, ok := request.BasicAuth()
			if !ok || clientID != "revoke-client" || request.FormValue("grant_type") != "client_credentials" {
				http.Error(writer, "bad token request", http.StatusBadRequest)
				return
			}
			mutex.Lock()
			tokenScope = request.FormValue("scope")
			mutex.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "revoke-token", "token_type": "Bearer", "expires_in": 300})
		case "/application-roles/revoke":
			if request.Header.Get("Authorization") != "Bearer revoke-token" || request.Header.Get("X-Tenant-ID") != "" || request.Header.Get("X-Request-ID") != "trace-revoke" {
				http.Error(writer, "bad revoke auth", http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"platform_user_id":"subject-1","application_code":"customer_portal","role_code":"portal_customer"}` {
				http.Error(writer, "bad revoke body", http.StatusBadRequest)
				return
			}
			mutex.Lock()
			requestKeys = append(requestKeys, request.Header.Get("Idempotency-Key"))
			requestNonces = append(requestNonces, request.Header.Get("X-Integration-Nonce"))
			mutex.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-revoke-1","data":{"platform_user_id":"subject-1","application_code":"customer_portal","role_code":"portal_customer","status":"DISABLED"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewHTTPPlatformRoleRevoker(context.Background(), PlatformRoleRevokerOptions{
		Endpoint: server.URL + "/application-roles/revoke", TokenURL: server.URL + "/oauth2/token",
		ClientID: "revoke-client", ClientSecret: "secret", Scope: applicationRoleRevokeScope,
		ApplicationCode: "customer_portal", HTTPClient: server.Client(),
		Now:         func() time.Time { return time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) },
		NonceReader: strings.NewReader(strings.Repeat("a", 32) + strings.Repeat("b", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestctx.WithID(context.Background(), "trace-revoke")
	for range 2 {
		if err = client.RevokePortalRole(ctx, "subject-1", "disable-operation-1"); err != nil {
			t.Fatal(err)
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if tokenScope != applicationRoleRevokeScope || len(requestKeys) != 2 || requestKeys[0] != "disable-operation-1" || requestKeys[1] != requestKeys[0] {
		t.Fatalf("scope=%q keys=%v", tokenScope, requestKeys)
	}
	if len(requestNonces) != 2 || requestNonces[0] == "" || requestNonces[1] == "" || requestNonces[0] == requestNonces[1] {
		t.Fatalf("nonces=%v", requestNonces)
	}
}

func TestHTTPPlatformRoleRevokerFailsClosedOnScopeResponseAndRedirect(t *testing.T) {
	t.Parallel()
	if _, err := NewHTTPPlatformRoleRevoker(context.Background(), PlatformRoleRevokerOptions{
		Endpoint: "https://identity.example/application-roles/revoke", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "revoke", ClientSecret: "secret", Scope: "application_role.revoke application_role.assign", ApplicationCode: "customer_portal",
	}); err == nil {
		t.Fatal("over-scoped role revocation client configuration was accepted")
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":300}`))
		case "/active":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-1","data":{"platform_user_id":"subject-1","application_code":"customer_portal","role_code":"portal_customer","status":"ACTIVE"}}`))
		case "/redirect":
			http.Redirect(writer, request, "/active", http.StatusTemporaryRedirect)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	for _, path := range []string{"/active", "/redirect"} {
		client, err := NewHTTPPlatformRoleRevoker(context.Background(), PlatformRoleRevokerOptions{
			Endpoint: server.URL + path, TokenURL: server.URL + "/oauth2/token", ClientID: "revoke", ClientSecret: "secret",
			Scope: applicationRoleRevokeScope, ApplicationCode: "customer_portal", HTTPClient: server.Client(),
			NonceReader: strings.NewReader(strings.Repeat("n", 32)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = client.RevokePortalRole(context.Background(), "subject-1", "disable-operation-1"); err == nil {
			t.Fatalf("revocation endpoint %s was accepted", path)
		}
	}
}
