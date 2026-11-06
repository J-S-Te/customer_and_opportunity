package portalbootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
)

// TestLocalPlatformUsesRealScopedReceipts 验证本地来源使用五职责OAuth及独立幂等域。
// 参数：t为测试上下文。
// 返回值：无；错误应用、未知回执、错误主体或假成功被接受时失败。
func TestLocalPlatformUsesRealScopedReceipts(t *testing.T) {
	scopes := map[string]int{}
	keys := map[string]string{}
	corrupt := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth2/token" {
			_ = r.ParseForm()
			scope := r.Form.Get("scope")
			scopes[scope]++
			_, _ = io.WriteString(w, `{"access_token":"`+scope+`","token_type":"Bearer","expires_in":300}`)
			return
		}
		scope := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if scopes[scope] == 0 || r.Header.Get("X-Integration-Nonce") == "" || r.Header.Get("X-Integration-Timestamp") == "" {
			t.Error("missing scoped proof")
		}
		key := r.Header.Get("Idempotency-Key")
		if !strings.HasPrefix(key, "portal-local-v1:") || len(key) > 128 {
			t.Error("CRM idempotency reused")
		}
		if old := keys[scope]; old != "" && old != key {
			t.Error("original operation changed key")
		}
		keys[scope] = key
		var payload map[string]string
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("bad payload")
		}
		data := map[string]string{"platform_user_id": "subject-real"}
		switch r.URL.Path {
		case "/api/v1/internal/external-users":
			if scope != "external_user.provision" || payload["mobile"] != "13800000000" {
				t.Error("wrong provision scope")
			}
			data["account_no"] = "PU-real"
		case "/api/v1/internal/application-roles", "/api/v1/internal/application-roles/revoke":
			status := "ACTIVE"
			expected := "application_role.assign"
			if strings.HasSuffix(r.URL.Path, "/revoke") {
				status = "DISABLED"
				expected = "application_role.revoke"
			}
			if scope != expected || payload["application_code"] != "customer_portal" || payload["role_code"] != "portal_customer" {
				t.Error("wrong role boundary")
			}
			data["application_code"], data["role_code"], data["status"] = "customer_portal", "portal_customer", status
		case "/api/v1/internal/external-users/subject-real/customer-binding", "/api/v1/internal/external-users/subject-real/customer-binding/disable":
			status := "ACTIVE"
			expected := "portal.identity_mapping.provision"
			if strings.HasSuffix(r.URL.Path, "/disable") {
				status = "DISABLED"
				expected = "portal.identity_mapping.disable"
			}
			if scope != expected || payload["customer_ref"] != "portal-local:v1:7:subject-real" {
				t.Error("wrong binding")
			}
			data["application_code"], data["status"] = "customer_portal", status
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if corrupt == "unknown" {
			data["unrecognized_success"] = "yes"
		} else if corrupt != "" {
			data[corrupt] = "wrong-receipt"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "OK", "message": "success", "request_id": "real-request", "data": data})
	}))
	defer server.Close()
	credential := func(id string) LocalIdentityCredential {
		return LocalIdentityCredential{ClientID: id, ClientSecret: "fixture-secret"}
	}
	options := LocalIdentityPlatformOptions{PlatformBaseURL: server.URL, TokenURL: server.URL + "/oauth2/token", TenantID: "tenant", Provision: credential("provision"), RoleAssign: credential("role-assign"), RoleRevoke: credential("role-revoke"), BindingWrite: credential("bind"), BindingDisable: credential("disable"), HTTPClient: server.Client()}
	platform, err := NewLocalIdentityPlatform(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	contact := localidentity.ContactInput{CustomerID: 7, DisplayName: "Fixture", Phone: "13800000000"}
	for i := 0; i < 2; i++ {
		identity, err := platform.Provision(context.Background(), "original-operation", contact)
		if err != nil || identity.PlatformUserID != "subject-real" {
			t.Fatal(identity, err)
		}
	}
	for _, action := range []func() error{func() error { return platform.AssignRole(context.Background(), "subject-real", "original-operation") }, func() error {
		return platform.BindCustomer(context.Background(), "subject-real", 7, "original-operation")
	}, func() error { return platform.RevokeRole(context.Background(), "subject-real", "original-operation") }, func() error {
		return platform.DisableBinding(context.Background(), "subject-real", 7, "original-operation")
	}} {
		if err := action(); err != nil {
			t.Fatal(err)
		}
	}
	if len(scopes) != 5 || len(keys) != 5 {
		t.Fatal("missing precise consumer", scopes)
	}
	for _, field := range []string{"platform_user_id", "application_code", "status"} {
		corrupt = field
		if err := platform.BindCustomer(context.Background(), "subject-real", 7, "original-operation"); err == nil {
			t.Fatalf("binding accepted mismatched %s", field)
		}
		if err := platform.DisableBinding(context.Background(), "subject-real", 7, "original-operation"); err == nil {
			t.Fatalf("disable accepted mismatched %s", field)
		}
	}
	corrupt = "unknown"
	if _, err := platform.Provision(context.Background(), "original-operation", contact); err == nil {
		t.Fatal("unknown receipt accepted")
	}
	options.RoleAssign = options.Provision
	if _, err := NewLocalIdentityPlatform(context.Background(), options); err == nil {
		t.Fatal("mixed credential accepted")
	}
	if _, err := NewLocalIdentityPlatform(context.Background(), LocalIdentityPlatformOptions{}); err == nil {
		t.Fatal("missing dependency accepted")
	}
}
