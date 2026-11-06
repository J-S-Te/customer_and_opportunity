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

// handoverMachinePrincipal 模拟平台交接机器令牌验签后的主体：
// 权限码来自签名 scope，交接五类端点要求 handover.machine。
func handoverMachinePrincipal(tenantID string, scopes ...string) *auth.Principal {
	permissions := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		permissions[scope] = struct{}{}
	}
	return &auth.Principal{UserID: "machine:platform-handover", TenantID: tenantID, Roles: []string{"machine"}, Permissions: permissions, ScopeMode: auth.ScopeAll}
}

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
	router := handoverTestRouter(t, handoverMachinePrincipal("tenant-machine", HandoverMachineScope))

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
// 认证中间件先于 scope 校验：缺少主体返回 401，有 scope 但无租户仍由处理器返回 403。
func TestHandoverEndpointsRequireMachineTenant(t *testing.T) {
	router := handoverTestRouter(t, nil)
	status, payload := handoverCall(t, router, http.MethodPost, "/internal/handover/freeze",
		`{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver"}`)
	if status != http.StatusUnauthorized || payload["code"] != "COMMON_UNAUTHENTICATED" {
		t.Fatalf("missing machine identity → %d %v", status, payload)
	}

	router = handoverTestRouter(t, handoverMachinePrincipal("", HandoverMachineScope))
	status, payload = handoverCall(t, router, http.MethodPost, "/internal/handover/freeze",
		`{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver"}`)
	if status != http.StatusForbidden || payload["code"] != "TENANT_REQUIRED" {
		t.Fatalf("missing machine tenant → %d %v", status, payload)
	}
}

// 交接写接口此前用 ShouldBindJSON（无体积上限、不拒未知字段）。改为统一严格解码后，
// 未知字段、重复键和超大请求体都必须在进入服务层前按交接校验错误（422）拒绝。
func TestHandoverWritesRejectNonStrictJSONBodies(t *testing.T) {
	router := handoverTestRouter(t, handoverMachinePrincipal("tenant-machine", HandoverMachineScope))
	bodies := map[string]string{
		"unknown field": `{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver","unexpected":"secret"}`,
		"duplicate key": `{"batch_id":"batch-1","batch_id":"batch-2","request_id":"request-1","user_id":"user-leaver"}`,
		"over limit":    `{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver","padding":"` + strings.Repeat("x", 33<<10) + `"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			status, payload := handoverCall(t, router, http.MethodPost, "/internal/handover/freeze", body)
			if status != http.StatusUnprocessableEntity || payload["code"] != "HANDOVER_VALIDATION" {
				t.Fatalf("case=%s → %d %v", name, status, payload)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			for _, leaked := range []string{"secret", "unexpected"} {
				if strings.Contains(string(encoded), leaked) {
					t.Fatalf("decoder details leaked %q in %s", leaked, encoded)
				}
			}
		})
	}
}

// BIZ-XAPI-01：交接五类端点除机器身份外必须持有 handover.machine scope，
// 其它合法机器 scope 不能驱动离职冻结/责任转移。
func TestHandoverEndpointsRequireHandoverMachineScope(t *testing.T) {
	router := handoverTestRouter(t, handoverMachinePrincipal("tenant-machine", "opportunity.status.write"))
	for _, call := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/internal/handover/freeze", `{"batch_id":"batch-1","request_id":"request-1","user_id":"user-leaver"}`},
		{http.MethodGet, "/internal/handover/responsibilities?user_id=user-leaver", ""},
		{http.MethodPost, "/internal/handover/transfers", `{"item_id":"item-1"}`},
		{http.MethodGet, "/internal/handover/transfers/item-1?idempotency_key=key-1", ""},
		{http.MethodPost, "/internal/handover/complete", `{"batch_id":"batch-1","outcome":"COMPLETED"}`},
	} {
		status, payload := handoverCall(t, router, call.method, call.path, call.body)
		if status != http.StatusForbidden || payload["code"] != "COMMON_FORBIDDEN" {
			t.Fatalf("%s %s without handover.machine → %d %v", call.method, call.path, status, payload)
		}
	}

	// 同一批请求在携带 handover.machine 时不再被权限层拦截（业务结果由处理器决定）。
	router = handoverTestRouter(t, handoverMachinePrincipal("tenant-machine", HandoverMachineScope))
	status, payload := handoverCall(t, router, http.MethodGet, "/internal/handover/responsibilities?user_id=user-leaver", "")
	if status == http.StatusForbidden {
		t.Fatalf("handover.machine scope rejected → %d %v", status, payload)
	}
}

// BIZ-CRM-05：转移必须证明责任项属于冻结批次。
// 未冻结的批次返回 409 HANDOVER_BATCH_NOT_FROZEN，且不落回执（避免同键重放永久卡死）。
func TestHandoverTransferRequiresFrozenBatch(t *testing.T) {
	router := handoverTestRouter(t, handoverMachinePrincipal("tenant-machine", HandoverMachineScope))

	transfer := func(batchID, itemID, key string) (int, map[string]any) {
		return handoverCall(t, router, http.MethodPost, "/internal/handover/transfers",
			`{"batch_id":"`+batchID+`","item_id":"`+itemID+`","request_id":"request-1","resource_type":"customer",`+
				`"resource_id":"customer:1","from_user_id":"user-leaver","to_user_id":"user-successor",`+
				`"expected_version":3,"idempotency_key":"`+key+`"}`)
	}

	status, payload := transfer("batch-unknown", "item-1", "handover:batch-unknown:item-1")
	if status != http.StatusConflict || payload["code"] != "HANDOVER_BATCH_NOT_FROZEN" {
		t.Fatalf("transfer without frozen batch → %d %v", status, payload)
	}
	status, payload = handoverCall(t, router, http.MethodGet,
		"/internal/handover/transfers/item-1?idempotency_key=handover:batch-unknown:item-1", "")
	if status != http.StatusNotFound {
		t.Fatalf("rejected transfer must not leave a receipt → %d %v", status, payload)
	}

	// 冻结该批次后同一责任项可以转移。
	status, payload = handoverCall(t, router, http.MethodPost, "/internal/handover/freeze",
		`{"batch_id":"batch-ok","request_id":"request-1","user_id":"user-leaver"}`)
	if status != http.StatusOK {
		t.Fatalf("freeze → %d %v", status, payload)
	}
	status, payload = transfer("batch-ok", "item-2", "handover:batch-ok:item-2")
	if status != http.StatusOK {
		t.Fatalf("frozen batch transfer → %d %v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data["status"] != ReceiptApplied {
		t.Fatalf("transfer receipt = %v", payload)
	}

	// 已释放的批次不能再驱动新的改派。
	if status, payload = handoverCall(t, router, http.MethodPost, "/internal/handover/complete",
		`{"batch_id":"batch-ok","outcome":"COMPLETED"}`); status != http.StatusOK {
		t.Fatalf("complete → %d %v", status, payload)
	}
	status, payload = transfer("batch-ok", "item-3", "handover:batch-ok:item-3")
	if status != http.StatusConflict || payload["code"] != "HANDOVER_BATCH_NOT_FROZEN" {
		t.Fatalf("released batch transfer → %d %v", status, payload)
	}
}
