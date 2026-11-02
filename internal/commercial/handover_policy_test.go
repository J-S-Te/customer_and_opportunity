package commercial

import (
	"context"
	"net/http/httptest"
	"testing"

	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
)

func TestCRMHistoryLicenseRetainsExactHandoverReadsAndTransferFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		method, path, request string
		operation             core.Operation
		want                  int
	}{
		{"GET", "/internal/v1/handover/responsibilities", "/internal/v1/handover/responsibilities", core.READ_HISTORY, 204},
		{"GET", "/internal/v1/handover/transfers/:itemID", "/internal/v1/handover/transfers/old-item", core.READ_HISTORY, 204},
		{"GET", "/api/v1/contract-transfer-outbox/failed", "/api/v1/contract-transfer-outbox/failed", core.READ_HISTORY, 204},
		{"POST", "/api/v1/contract-transfer-outbox/failed/:id/retry", "/api/v1/contract-transfer-outbox/failed/old/retry", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/freeze", "/internal/v1/handover/freeze", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/transfers", "/internal/v1/handover/transfers", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/complete", "/internal/v1/handover/complete", core.MUTATE_BUSINESS, 403},
		{"GET", "/internal/v1/handover/new-effect", "/internal/v1/handover/new-effect", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/responsibilities", "/internal/v1/handover/responsibilities", core.MUTATE_BUSINESS, 403},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			if got := Operation("crm", tc.method, tc.path); got != tc.operation {
				t.Errorf("classification=%s want=%s", got, tc.operation)
			}
			r := gin.New()
			base := r.Group("/crm-custom-prefix", Middleware(func(_ context.Context, op core.Operation) error {
				if op == core.MUTATE_BUSINESS {
					return core.ErrDenied
				}
				return nil
			}, "crm", "/crm-custom-prefix"))
			base.Handle(tc.method, tc.path, func(c *gin.Context) { c.Status(204) })
			response := httptest.NewRecorder()
			r.ServeHTTP(response, httptest.NewRequest(tc.method, "/crm-custom-prefix"+tc.request, nil))
			if response.Code != tc.want {
				t.Errorf("history-only HTTP status=%d want=%d", response.Code, tc.want)
			}
		})
	}
}
