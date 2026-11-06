package portalbootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// TestLocalFilesExactBindingAndCompletedReplay 验证真实v2完成重放仍能恢复本地来源。
// 参数：t为测试上下文。
// 返回值：无；使用旧项目来源、假绑定或丢失完成重放时失败。
func TestLocalFilesExactBindingAndCompletedReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/upload-sessions" || r.Method != "POST" {
			t.Errorf("unexpected upload %s", r.URL.Path)
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["purpose"] != "portal.local_report" || payload["classification"] != "CONFIDENTIAL" || payload["resource_type"] != "PORTAL_REPORT_REQUEST" || payload["resource_id"] != "7" || payload["binding_type"] != "REPORT" || payload["idempotency_key"] != "portal-local-v1:operation" {
			t.Errorf("incorrect scope: %+v", payload)
		}
		if actor, exists := payload["actor_user_id"]; exists && actor != "" {
			t.Errorf("machine upload must not claim human actor: %v", actor)
		}
		_, _ = io.WriteString(w, `{"data":{"upload_id":"upload-1","file_id":"file-1","status":"READY"}}`)
	}))
	defer server.Close()
	client, e := filegatewayclient.New(server.URL, server.Client(), func(context.Context) (string, error) { return "portal-writer", nil })
	if e != nil {
		t.Fatal(e)
	}
	files, e := NewLocalIdentityFiles(client, "portal-app", "portal-app")
	if e != nil {
		t.Fatal(e)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{UserID: "admin", Roles: []string{"portal_super_admin"}, Permissions: map[string]struct{}{localidentity.PermissionReportManage: {}}})
	descriptor, e := files.Upload(ctx, 7, "operation", "report.pdf", strings.NewReader("%PDF-original"))
	if e != nil || descriptor.SourceType != "PORTAL_LOCAL" || descriptor.ObjectRef != "file-1" || descriptor.Size != 13 {
		t.Fatalf("completed replay lost: %+v err=%v", descriptor, e)
	}
	if _, e := NewLocalIdentityFiles(client, "other-app", "portal-app"); e == nil {
		t.Fatal("mismatched application accepted")
	}
	if _, e := NewLocalIdentityFiles(nil, "portal-app", "portal-app"); e == nil {
		t.Fatal("nil dependency accepted")
	}
}
