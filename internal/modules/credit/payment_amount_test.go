package credit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func paymentPrincipalContext() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
}

func basePaymentEvent() PaymentEvent {
	return PaymentEvent{EventID: "evt-1", PaymentID: "pay-1", CustomerID: 1, DueDate: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), DueAmount: "100.00", PaidAmount: "100.00", SourceSystem: "settlement"}
}

// due_amount/paid_amount 是任意长字符串输入的入口：超过列宽（32）或含非数字字符时，
// 必须在进入 decimal 解析前以 422 拒绝，不能把任意长输入当金额承载。
func TestProcessPaymentRejectsAmountsBeyondLengthOrCharacterSet(t *testing.T) {
	service := &Service{}
	ctx := paymentPrincipalContext()
	patches := map[string]func(*PaymentEvent){
		"due over 32":    func(event *PaymentEvent) { event.DueAmount = strings.Repeat("9", maxCreditAmountLength+1) },
		"paid over 32":   func(event *PaymentEvent) { event.PaidAmount = strings.Repeat("9", maxCreditAmountLength+1) },
		"due exponent":   func(event *PaymentEvent) { event.DueAmount = "1e3" },
		"paid letters":   func(event *PaymentEvent) { event.PaidAmount = "abc" },
		"due spaces":     func(event *PaymentEvent) { event.DueAmount = " 100.00" },
		"empty paid":     func(event *PaymentEvent) { event.PaidAmount = "" },
		"due injection":  func(event *PaymentEvent) { event.DueAmount = "1.0;DROP" },
		"paid huge tail": func(event *PaymentEvent) { event.PaidAmount = "1." + strings.Repeat("0", maxCreditAmountLength) },
	}
	for name, patch := range patches {
		t.Run(name, func(t *testing.T) {
			event := basePaymentEvent()
			patch(&event)
			record, err := service.ProcessPayment(ctx, event)
			if err == nil {
				t.Fatalf("payment accepted: %#v", record)
			}
			appErr := apperror.As(err)
			if appErr.HTTPStatus != http.StatusUnprocessableEntity || appErr.Code != "CRM_CREDIT_PAYMENT_AMOUNT_INVALID" {
				t.Fatalf("err=%v status=%d code=%q", err, appErr.HTTPStatus, appErr.Code)
			}
		})
	}
}

// 恰好等于列宽的金额字符串必须通过金额门禁，继续走后续事件校验（这里用非法 source_system
// 证明已越过门禁），避免上限误伤合法边界值。
func TestProcessPaymentAcceptsAmountAtLengthBoundary(t *testing.T) {
	service := &Service{}
	event := basePaymentEvent()
	event.DueAmount = strings.Repeat("9", maxCreditAmountLength)
	event.SourceSystem = ""
	_, err := service.ProcessPayment(paymentPrincipalContext(), event)
	appErr := apperror.As(err)
	if appErr.Code != "CRM_CREDIT_EVENT_INVALID" {
		t.Fatalf("err=%v code=%q, want the event-level rejection after the amount gate", err, appErr.Code)
	}
}

func creditStrictJSONRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	principal := auth.Principal{UserID: "actor-a", TenantID: "tenant-a", Permissions: map[string]struct{}{
		"customer.credit.payment.ingest": {}, "customer.credit.apply": {},
		"customer.credit.rule.manage": {}, "customer.credit.approve": {},
	}}
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	handler := NewHandler(&Service{})
	router.POST("/credit/payment-events", handler.Payment)
	router.POST("/customers/:id/credit/applications", handler.Apply)
	router.PUT("/credit/rule-settings", handler.UpdateRuleSettings)
	router.POST("/credit/applications/:applicationID/approve", handler.Approve)
	return router
}

func creditCall(t *testing.T, router *gin.Engine, method, path, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// 信用写接口此前用 ShouldBindJSON（无体积上限、不拒未知字段）。改为统一严格解码后，
// 未知字段、重复键和超大请求体都必须在进入服务层前被拒绝，且不反射解析器细节。
func TestCreditWriteEndpointsRejectNonStrictJSONBodies(t *testing.T) {
	router := creditStrictJSONRouter()
	oversized := `{"event_id":"evt-1","padding":"` + strings.Repeat("x", 17<<10) + `"}`
	cases := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode string
	}{
		{
			name: "payment unknown field", method: http.MethodPost, path: "/credit/payment-events",
			body: `{"event_id":"evt-1","payment_id":"pay-1","customer_id":1,"due_date":"2026-08-31T00:00:00Z","due_amount":"1","paid_amount":"1","unexpected":"secret"}`,
		},
		{
			name: "payment duplicate key", method: http.MethodPost, path: "/credit/payment-events",
			body: `{"event_id":"evt-1","event_id":"evt-2","payment_id":"pay-1","customer_id":1,"due_date":"2026-08-31T00:00:00Z","due_amount":"1","paid_amount":"1"}`,
		},
		{
			name: "payment oversized body", method: http.MethodPost, path: "/credit/payment-events",
			body: oversized,
		},
		{
			name: "application unknown field", method: http.MethodPost, path: "/customers/9/credit/applications",
			body: `{"target_level":"A","reason":"调整","unexpected":"secret"}`, wantCode: "CRM_CREDIT_APPLICATION_INVALID",
		},
		{
			name: "rule settings unknown field", method: http.MethodPut, path: "/credit/rule-settings",
			body:     `{"grace_days":7,"on_time_threshold":2,"late_threshold":2,"level_step":1,"enabled":true,"unexpected":"secret"}`,
			wantCode: "CRM_CREDIT_RULE_INVALID",
		},
		{
			name: "decision unknown field", method: http.MethodPost, path: "/credit/applications/5/approve",
			body: `{"opinion":"同意","version":1,"unexpected":"secret"}`, wantCode: "CRM_CREDIT_APPLICATION_INVALID",
		},
		{
			name: "payment amount over column width", method: http.MethodPost, path: "/credit/payment-events",
			body:     `{"event_id":"evt-1","payment_id":"pay-1","customer_id":1,"due_date":"2026-08-31T00:00:00Z","due_amount":"` + strings.Repeat("9", 33) + `","paid_amount":"1"}`,
			wantCode: "CRM_CREDIT_PAYMENT_AMOUNT_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := creditCall(t, router, tc.method, tc.path, tc.body)
			wantStatus := http.StatusBadRequest
			if tc.wantCode == "CRM_CREDIT_PAYMENT_AMOUNT_INVALID" {
				// 金额门禁在服务层，按请求校验失败返回 422。
				wantStatus = http.StatusUnprocessableEntity
			}
			if status != wantStatus {
				t.Fatalf("status=%d body=%s, want %d", status, body, wantStatus)
			}
			if tc.wantCode != "" && !strings.Contains(body, tc.wantCode) {
				t.Fatalf("body=%s, want code %s", body, tc.wantCode)
			}
			for _, leaked := range []string{"secret", "unexpected"} {
				if strings.Contains(body, leaked) {
					t.Fatalf("decoder details leaked %q in %s", leaked, body)
				}
			}
		})
	}
}
