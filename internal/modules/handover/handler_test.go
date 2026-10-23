package handover

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// 端点必须只接受机器身份里的租户：请求体或查询参数都不能扩大范围。
func handoverTestRouter(t *testing.T, principal *auth.Principal) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if principal != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), *principal))
		}
		c.Next()
	})
	repository := newRepositoryFake("customer:1")
	service := serviceFor(t, repository)
	RegisterInternalRoutes(router.Group("/internal"), NewHandler(service))
	return router
}

func handoverCall(t *testing.T, router *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	payload := map[string]any{}
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	return response.Code, payload
}

func TestHandoverEndpointsBindTenantFromMachineIdentity(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-machine"}
	router := handoverTestRouter(t, &principal)

	status, payload := handoverCall(t, router, http.MethodPost, "/internal/handover/freeze",
		`{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver"}`)
	if status != http.StatusOK {
		t.Fatalf("freeze → %d %v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data["system"] != "crm" || data["frozen"] != true {
		t.Fatalf("freeze payload = %v", payload)
	}

	status, payload = handoverCall(t, router, http.MethodPost, "/internal/handover/freeze",
		`{"tenant_id":"tenant-attacker","batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver"}`)
	if status != http.StatusForbidden || payload["code"] != "TENANT_MISMATCH" {
		t.Fatalf("tenant override → %d %v", status, payload)
	}

	status, payload = handoverCall(t, router, http.MethodGet, "/internal/handover/responsibilities?user_id=user-leaver", "")
	if status != http.StatusOK {
		t.Fatalf("responsibilities → %d %v", status, payload)
	}

	status, payload = handoverCall(t, router, http.MethodPost, "/internal/handover/transfers",
		`{"item_id":"item-1"}`)
	if status != http.StatusUnprocessableEntity || payload["code"] != "HANDOVER_VALIDATION" {
		t.Fatalf("incomplete transfer → %d %v", status, payload)
	}

	status, payload = handoverCall(t, router, http.MethodPost, "/internal/handover/complete",
		`{"batch_id":"batch-1","outcome":"COMPLETED"}`)
	if status != http.StatusOK {
		t.Fatalf("complete → %d %v", status, payload)
	}
}

// 没有机器身份时不得放行（中间件缺失属配置错误，端点自身也要 失败时安全拒绝）。
func TestHandoverEndpointsRequireMachineTenant(t *testing.T) {
	router := handoverTestRouter(t, nil)
	status, payload := handoverCall(t, router, http.MethodPost, "/internal/handover/freeze",
		`{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver"}`)
	if status != http.StatusForbidden || payload["code"] != "TENANT_REQUIRED" {
		t.Fatalf("missing machine tenant → %d %v", status, payload)
	}
}
