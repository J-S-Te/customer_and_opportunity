package portalinvite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 平台客户绑定双写适配器：PUT 建立绑定、POST 禁用绑定，路径携带 platform_user_id，
// scope 固定为 portal_mapping_provision / portal_mapping_disable，且与门户映射调用共用凭证。
func TestHTTPPlatformBindingAdaptersUseExactScopesMethodsAndProof(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	scopes := make(map[string]string)
	var bindMethod, disableMethod, bindKey, disableKey, bindNonce, disableNonce string
	var bindPath, disablePath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			clientID, _, ok := request.BasicAuth()
			if !ok {
				http.Error(writer, "bad token request", http.StatusBadRequest)
				return
			}
			mutex.Lock()
			scopes[clientID] = request.FormValue("scope")
			mutex.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": clientID + "-token", "token_type": "Bearer", "expires_in": 300})
		case "/api/v1/internal/external-users/subject-1/customer-binding":
			if request.Header.Get("Authorization") != "Bearer bind-client-token" {
				http.Error(writer, "bad bind auth", http.StatusUnauthorized)
				return
			}
			bindMethod, bindKey, bindNonce = request.Method, request.Header.Get("Idempotency-Key"), request.Header.Get("X-Integration-Nonce")
			bindPath = request.URL.Path
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-1","data":{"platform_user_id":"subject-1","application_code":"customer_portal","status":"ACTIVE"}}`))
		case "/api/v1/internal/external-users/subject-1/customer-binding/disable":
			if request.Header.Get("Authorization") != "Bearer disable-client-token" {
				http.Error(writer, "bad disable auth", http.StatusUnauthorized)
				return
			}
			disableMethod, disableKey, disableNonce = request.Method, request.Header.Get("Idempotency-Key"), request.Header.Get("X-Integration-Nonce")
			disablePath = request.URL.Path
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"platform-2","data":{"platform_user_id":"subject-1","application_code":"customer_portal","status":"DISABLED"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	writer, err := NewHTTPPlatformBindingWriter(context.Background(), PlatformBindingWriterOptions{
		BaseURL: server.URL + "/api/v1/internal/external-users", TokenURL: server.URL + "/oauth2/token",
		ClientID: "bind-client", ClientSecret: "secret", Scope: portalMappingProvisionScope, ApplicationCode: "customer_portal",
		HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC) },
		NonceReader: strings.NewReader(strings.Repeat("b", 32) + strings.Repeat("n", 96)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.BindCustomerIdempotent(context.Background(), "subject-1", "2001", "bind-key-1"); err != nil {
		t.Fatalf("BindCustomerIdempotent() error = %v", err)
	}
	disabler, err := NewHTTPPlatformBindingDisabler(context.Background(), PlatformBindingDisablerOptions{
		BaseURL: server.URL + "/api/v1/internal/external-users", TokenURL: server.URL + "/oauth2/token",
		ClientID: "disable-client", ClientSecret: "secret", Scope: portalMappingDisableScope, ApplicationCode: "customer_portal",
		HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC) },
		NonceReader: strings.NewReader(strings.Repeat("d", 32) + strings.Repeat("n", 96)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = disabler.DisableCustomerBindingIdempotent(context.Background(), "subject-1", "2001", "disable-key-1"); err != nil {
		t.Fatalf("DisableCustomerBindingIdempotent() error = %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if scopes["bind-client"] != portalMappingProvisionScope || scopes["disable-client"] != portalMappingDisableScope {
		t.Fatalf("scopes = %v", scopes)
	}
	if bindMethod != http.MethodPut || disableMethod != http.MethodPost {
		t.Fatalf("methods = bind:%s disable:%s", bindMethod, disableMethod)
	}
	if bindPath != "/api/v1/internal/external-users/subject-1/customer-binding" || disablePath != "/api/v1/internal/external-users/subject-1/customer-binding/disable" {
		t.Fatalf("paths = %q / %q", bindPath, disablePath)
	}
	if bindKey != "bind-key-1" || disableKey != "disable-key-1" || bindNonce == "" || disableNonce == "" {
		t.Fatalf("proof headers = bind:%q/%q disable:%q/%q", bindKey, bindNonce, disableKey, disableNonce)
	}
}

func TestHTTPPlatformBindingAdaptersRejectInvalidConfiguration(t *testing.T) {
	t.Parallel()
	if _, err := NewHTTPPlatformBindingWriter(context.Background(), PlatformBindingWriterOptions{
		BaseURL: "https://identity.example/api/v1/internal/external-users", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "bind", ClientSecret: "secret", Scope: portalMappingDisableScope, ApplicationCode: "customer_portal",
	}); err == nil {
		t.Fatal("writer accepted the disable scope")
	}
	if _, err := NewHTTPPlatformBindingDisabler(context.Background(), PlatformBindingDisablerOptions{
		BaseURL: "https://identity.example/api/v1/internal/external-users", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "disable", ClientSecret: "secret", Scope: portalMappingProvisionScope, ApplicationCode: "customer_portal",
	}); err == nil {
		t.Fatal("disabler accepted the provision scope")
	}
}

func TestHTTPPlatformBindingWriterRejectsInvalidRequest(t *testing.T) {
	t.Parallel()
	writer := &HTTPPlatformBindingWriter{client: &http.Client{}}
	if err := writer.BindCustomerIdempotent(context.Background(), "subject-1", "  ", "key"); err == nil {
		t.Fatal("blank customer ref accepted")
	}
	if err := writer.BindCustomerIdempotent(context.Background(), "", "2001", "key"); err == nil {
		t.Fatal("blank platform user accepted")
	}
	if err := writer.BindCustomerIdempotent(context.Background(), "subject-1", strings.Repeat("x", 65), "key"); err == nil {
		t.Fatal("overlong customer ref accepted")
	}
}
