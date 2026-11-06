package customer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

// newExportHandlerFixture 构造一个可观察 ListQuery 的导出处理器；仓储桩只返回一页，
// 足以断言 handler 到 service 的筛选映射。
func newExportHandlerFixture(t *testing.T) (*Handler, *exportRepositoryStub) {
	t.Helper()
	repository := &exportRepositoryStub{page: pagination.Page[Response]{
		Items: []Response{{
			ID: 1, CustomerNo: "KH202609040001", Name: "示例客户", Status: StatusActive,
			CreatedAt: time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC),
		}},
		Page: 1, PageSize: createExportPageSize, Total: 1,
	}}
	service := (&Service{repo: repository, now: func() time.Time { return time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC) }}).
		UseOwnerDirectory(&ownerCatalogProbe{users: map[string]ownerdirectory.User{}}).
		UseFreezeChecker(allowAllFreezeChecker{})
	return NewHandler(service), repository
}

func callCreateExport(t *testing.T, handler *Handler, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/customer-exports", body)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"}))
	context.Request = request
	handler.CreateExport(context)
	return recorder
}

func TestCustomerCreateExportHandlerForwardsFilters(t *testing.T) {
	handler, repository := newExportHandlerFixture(t)
	body := `{"filters":{
		"keyword":"示例","type":"企业","industry":"软件","region":"华东","owner_id":"owner-1",
		"status":"ACTIVE","credit_level":"A","quick_filter":"NEW",
		"created_from":"2026-08-01T00:00:00+08:00","created_to":"2026-09-01T00:00:00+08:00",
		"last_followup_from":"2026-08-10T00:00:00+08:00","last_followup_to":"2026-08-20T00:00:00+08:00",
		"sort_by":"name","sort_order":"asc","page":3,"page_size":20}}`
	recorder := callCreateExport(t, handler, strings.NewReader(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(repository.queries) != 1 {
		t.Fatalf("List() calls = %d, want 1", len(repository.queries))
	}
	got := repository.queries[0]
	if got.Keyword != "示例" || got.CustomerType != "企业" || got.Industry != "软件" || got.Region != "华东" || got.OwnerID != "owner-1" || got.Status != StatusActive || got.CreditLevel != "A" || got.QuickFilter != QuickFilterNew {
		t.Fatalf("filters not forwarded: %#v", got)
	}
	// 前端按北京日历日换算后传 RFC3339，服务端按瞬间解析并统一转 UTC。
	wantCreatedFrom := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	wantCreatedTo := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	if got.CreatedFrom == nil || !got.CreatedFrom.Equal(wantCreatedFrom) || got.CreatedTo == nil || !got.CreatedTo.Equal(wantCreatedTo) {
		t.Fatalf("created range = %v~%v, want %v~%v", got.CreatedFrom, got.CreatedTo, wantCreatedFrom, wantCreatedTo)
	}
	wantFollowupFrom := time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC)
	wantFollowupTo := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	if got.LastFollowupFrom == nil || !got.LastFollowupFrom.Equal(wantFollowupFrom) || got.LastFollowupTo == nil || !got.LastFollowupTo.Equal(wantFollowupTo) {
		t.Fatalf("last followup range = %v~%v, want %v~%v", got.LastFollowupFrom, got.LastFollowupTo, wantFollowupFrom, wantFollowupTo)
	}
	if got.SortBy != "name" || got.SortOrder != "asc" {
		t.Fatalf("sort = %q %q, want name asc", got.SortBy, got.SortOrder)
	}
	// filters.page/page_size 被忽略：导出自己分页并固定 100 条/页。
	if got.Page != 1 || got.PageSize != createExportPageSize {
		t.Fatalf("pagination = %d/%d, want 1/%d", got.Page, got.PageSize, createExportPageSize)
	}
}

func TestCustomerCreateExportHandlerAcceptsEmptyBodyForLegacyCallers(t *testing.T) {
	handler, repository := newExportHandlerFixture(t)
	recorder := callCreateExport(t, handler, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(repository.queries) != 1 {
		t.Fatalf("List() calls = %d, want 1", len(repository.queries))
	}
	got := repository.queries[0]
	if got.Keyword != "" || got.CustomerType != "" || got.Status != "" || got.CreatedFrom != nil || got.CreatedTo != nil {
		t.Fatalf("empty body produced filters: %#v", got)
	}
	if got.Page != 1 || got.PageSize != createExportPageSize || got.SortBy != "created_at" || got.SortOrder != "asc" {
		t.Fatalf("default export query = %#v", got)
	}
}

func TestCustomerCreateExportHandlerRejectsUnsafeBodyBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	tests := map[string]string{
		"malformed":          `{`,
		"array":              `[{"filters":{}}]`,
		"null":               `null`,
		"scalar":             `"filters"`,
		"unknown filter key": `{"filters":{"page_size":"20","tenant_id":"other"}}`,
		"unknown top key":    `{"filters":{},"other":1}`,
		"trailing content":   `{"filters":{}} {}`,
		"over one MiB":       `{"filters":{"keyword":"` + strings.Repeat("x", (1<<20)+32) + `"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := callCreateExport(t, handler, strings.NewReader(body))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_INVALID_ARGUMENT") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCustomerCreateExportHandlerRejectsNonRFC3339Dates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	tests := map[string]string{
		"date only from":        `{"filters":{"created_from":"2026-08-01"}}`,
		"invalid time to":       `{"filters":{"created_to":"2026-08-01T25:00:00Z"}}`,
		"invalid followup from": `{"filters":{"last_followup_from":"yesterday"}}`,
		"invalid followup to":   `{"filters":{"last_followup_to":"2026/08/20"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := callCreateExport(t, handler, strings.NewReader(body))
			if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "CRM_CUSTOMER_EXPORT_FILTERS_INVALID") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
