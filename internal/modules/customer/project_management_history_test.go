package customer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProjectManagementHistoryReadsScopedSummary 验证 CRM 使用独立凭据读取本客户分页。
// 参数：t 为测试上下文。
// 返回值：无。
func TestProjectManagementHistoryReadsScopedSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			id, secret, ok := r.BasicAuth()
			if !ok || id != "crm-reader" || secret != "fixture" ||
				r.FormValue("scope") != "crm.project_history.read" || r.FormValue("grant_type") != "client_credentials" {
				t.Error("unexpected machine credentials or scope")
			}
			_, _ = io.WriteString(w, `{"access_token":"machine","token_type":"Bearer","expires_in":3600}`)
		case "/internal/v1/crm/customers/42/projects":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer machine" ||
				r.URL.RawQuery != "page=2&page_size=1" || r.Header.Get("X-Tenant-ID") != "" {
				t.Error("unexpected scoped project request")
			}
			_, _ = io.WriteString(w, `{"data":{"items":[{"project_id":"P-1","project_name":"项目",
				"contract_no":"C-1","status":"实施中","progress_pct":40,"due":"2026-12-01",
				"source_updated_at":"2026-11-19T00:00:00Z"}],
				"tenant_id":"tenant-a","customer_id":"42","page":2,"page_size":1,"total":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	reader, err := NewProjectManagementHistoryReader(context.Background(), ProjectHistoryReaderOptions{
		Endpoint: server.URL + "/internal/v1/crm/customers", TokenURL: server.URL + "/oauth2/token",
		ClientID: "crm-reader", ClientSecret: "fixture", Scope: "crm.project_history.read",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.ListCustomerProjects(context.Background(), "tenant-a", 42, 2, 1)
	if err != nil || result.Source != "project_management" || len(result.Items) != 1 ||
		result.Items[0].Due != "2026-12-01" || !result.Items[0].SyncedAt.IsZero() {
		t.Fatalf("history=%+v err=%v", result, err)
	}
	payload, err := json.Marshal(projectHistoryHTTPData(result))
	if err != nil || !strings.Contains(string(payload), `"source":"project_management"`) ||
		strings.Contains(string(payload), "tenant_id") || strings.Contains(string(payload), "synced_at") ||
		strings.Contains(string(payload), "stale") || strings.Contains(string(payload), "delayed") {
		t.Fatalf("unexpected public contract: %s (%v)", payload, err)
	}
	_, err = reader.ListCustomerProjects(context.Background(), "other-tenant", 42, 2, 1)
	if err == nil {
		t.Fatal("project management response from another tenant was accepted")
	}
}
