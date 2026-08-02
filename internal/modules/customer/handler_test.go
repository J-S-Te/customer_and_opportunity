package customer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCustomerFollowupHandlerRejectsInvalidIDBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "invalid"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/customers/invalid/followups", strings.NewReader(`{}`))
	handler.CreateFollowup(context)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_INVALID_ARGUMENT") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCustomerListHandlerRejectsUnknownAndInvalidPaginationBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, rawQuery := range []string{"tenant_id=other", "page=abc", "page=0", "page_size=0", "page_size=101"} {
		t.Run(rawQuery, func(t *testing.T) {
			handler := &Handler{}
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/customers?"+rawQuery, nil)
			handler.List(context)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "CRM_CUSTOMER_QUERY_INVALID") {
				t.Fatalf("query=%s status=%d body=%s", rawQuery, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCustomerListHandlerRejectsNonRFC3339BoundaryBeforeService(t *testing.T) {
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/customers?created_from=2026-08-01", nil)
	handler.List(context)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "CRM_CUSTOMER_QUERY_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCustomerWriteHandlersRejectUnsafeJSONWithGenericError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, path, body string
		invoke           func(*Handler, *gin.Context)
	}{
		{"stakeholders unknown", "/customers/1/stakeholders", `{"version":1,"reason":"x","items":[],"tenant_id":"other"}`, func(h *Handler, c *gin.Context) { h.ReplaceStakeholders(c) }},
		{"systems trailing", "/customers/1/systems", `{"version":1,"reason":"x","items":[]} {}`, func(h *Handler, c *gin.Context) { h.ReplaceInformationSystems(c) }},
		{"create empty", "/customers", ``, func(h *Handler, c *gin.Context) { h.Create(c) }},
		{"merge scalar", "/customers/merge", `null`, func(h *Handler, c *gin.Context) { h.Merge(c) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{}
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Params = gin.Params{{Key: "id", Value: "1"}}
			context.Request = httptest.NewRequest(http.MethodPut, test.path, strings.NewReader(test.body))
			test.invoke(handler, context)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"message":"invalid request"`) || strings.Contains(recorder.Body.String(), "unknown field") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCustomerWriteHandlerRejectsBodyOverOneMiB(t *testing.T) {
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(`{"name":"`+strings.Repeat("a", (1<<20)+32)+`"}`))
	handler.Create(context)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"message":"invalid request"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
