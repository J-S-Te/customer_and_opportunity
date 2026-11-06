package localidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// TestManagementPermissionFailsBeforeStorage 验证客户角色及缺精确权限不能触发目录读取。
// 参数：t为测试上下文。
// 返回值：无；未授权请求进入nil存储或获得成功时失败。
func TestManagementPermissionFailsBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, principal := range []auth.Principal{{}, {TenantID: "tenant", UserID: "customer", Roles: []string{"portal_customer"}, Permissions: map[string]struct{}{PermissionCustomerManage: {}}}, {TenantID: "tenant", UserID: "admin", Roles: []string{"portal_super_admin"}, DataScopes: []auth.DataScope{{RoleCode: "portal_super_admin", ScopeType: "APPLICATION"}}}} {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPrincipal(context.Background(), principal))
		})
		NewHandler(&Service{Dependencies: Dependencies{TenantID: "tenant", Mode: ModeLocal}}).RegisterRoutes(engine.Group("/local-admin"))
		for _, path := range []string{"/local-admin/customers", "/local-admin/operations"} {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != 403 {
				t.Fatalf("permission bypass %s status=%d", path, w.Code)
			}
		}
	}
}

// TestStrictManagementMetadataAndDurableError 验证未知字段与失败原操作号契约。
// 参数：t为测试上下文。
// 返回值：无；宽松元数据或失败伪成功时失败。
func TestStrictManagementMetadataAndDurableError(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"customer_id":7,"source_type":"CRM"}`, `{"customer_id":7} {}`, strings.Repeat(" ", 16385)} {
		var input ReportInput
		if decodeMetadata(raw, &input) {
			t.Fatalf("accepted invalid metadata %q", raw)
		}
	}
	var valid ReportInput
	if !decodeMetadata(`{"customer_id":7,"namespace":"portal-local","project_id":"P7","account_id":"real-subject","report_type":"PDF","reason":"真实来源"}`, &valid) {
		t.Fatal("valid metadata rejected")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/retry", bytes.NewReader(nil))
	replyOperation(c, OperationResult{Operation: Operation{OperationNo: strings.Repeat("a", 32), Stage: "IDENTITY_READY", Status: "RETRY_WAIT"}}, ErrUnavailable)
	if w.Code != 503 {
		t.Fatal("failed operation returned success", w.Code)
	}
	var envelope struct {
		Details struct {
			Operation Operation `json:"operation"`
		} `json:"details"`
	}
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Details.Operation.Stage != "IDENTITY_READY" || envelope.Details.Operation.OperationNo == "" {
		t.Fatal("durable recovery coordinates lost")
	}
}
