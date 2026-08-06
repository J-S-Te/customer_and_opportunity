package opportunity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func TestLifecycleHandlerRejectsInvalidIDBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	for _, call := range []struct {
		name string
		fn   gin.HandlerFunc
	}{{"void", handler.Void}, {"restore", handler.Restore}, {"update", handler.Update}} {
		t.Run(call.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Params = gin.Params{{Key: "id", Value: "invalid"}}
			context.Request = httptest.NewRequest(http.MethodPost, "/opportunities/invalid", strings.NewReader(`{}`))
			call.fn(context)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_INVALID_ARGUMENT") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestChangeOwnerHandlerRequiresIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Params = gin.Params{{Key: "id", Value: "7"}}
	request := httptest.NewRequest(http.MethodPut, "/opportunities/7/owner", strings.NewReader(`{"owner_user_id":"sub-new","version":1,"reason":"交接"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}))
	ginContext.Request = request
	(&Handler{service: &Service{}}).ChangeOwner(ginContext)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateHandlerRequiresIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/opportunities", strings.NewReader(`{"name":"n","customer_id":1,"type":"t","source":"s","expected_amount":"1","expected_sign_date":"2026-09-01","requirement_summary":"r"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}))
	ginContext.Request = request
	(&Handler{service: &Service{}}).Create(ginContext)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTeamHandlersRejectInvalidIDBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	for _, call := range []struct {
		name string
		fn   gin.HandlerFunc
	}{{"owner", handler.ChangeOwner}, {"members get", handler.GetMembers}, {"member terms", handler.ListMemberTerms}, {"members replace", handler.ReplaceMembers}} {
		t.Run(call.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Params = gin.Params{{Key: "id", Value: "invalid"}}
			context.Request = httptest.NewRequest(http.MethodPut, "/opportunities/invalid", strings.NewReader(`{}`))
			call.fn(context)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_INVALID_ARGUMENT") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestJSONWriteHandlerRejectsUnsafeBodiesWithoutLeakingParserDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := map[string]string{
		"unknown field":    `{"name":"n","customer_id":1,"type":"t","source":"s","expected_amount":"1","expected_sign_date":"2026-09-01","requirement_summary":"r","owner_user_id":"u","unexpected":"secret-value"}`,
		"trailing object":  `{"name":"n"}{"name":"second"}`,
		"empty body":       "",
		"missing required": `{"name":"n"}`,
		"over one MiB":     `{"name":"` + strings.Repeat("x", (1<<20)+1) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/opportunities", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			ginContext.Request = request

			(&Handler{}).Create(ginContext)

			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"COMMON_INVALID_ARGUMENT"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "secret-value") || strings.Contains(recorder.Body.String(), "unknown field") {
				t.Fatalf("parser or request detail leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestReadHandlersRejectUnknownAndRepeatedQueryParametersBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	tests := []struct {
		name     string
		path     string
		rawQuery string
		params   gin.Params
		call     gin.HandlerFunc
	}{
		{name: "rule list has no query dto", path: "/opportunity-stage-alert-rules?unexpected=1", call: handler.StageAlertRules},
		{name: "alert list unknown", path: "/opportunity-stage-alerts?status=UNREAD", call: handler.StageAlerts},
		{name: "alert list repeated", path: "/opportunity-stage-alerts?page=1&page=2", call: handler.StageAlerts},
		{name: "detail has no query dto", path: "/opportunities/7?include=members", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.Get},
		{name: "list unknown", path: "/opportunities?customer_id=8", call: handler.List},
		{name: "list repeated", path: "/opportunities?stage=报价&stage=投标", call: handler.List},
		{name: "board only accepts filters", path: "/opportunities/board?page=1", call: handler.Board},
		{name: "members unknown", path: "/opportunities/7/members?page=1", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.GetMembers},
		{name: "members repeated", path: "/opportunities/7/members?include_inactive=true&include_inactive=false", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.GetMembers},
		{name: "member terms unknown", path: "/opportunities/7/member-terms?sort=asc", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.ListMemberTerms},
		{name: "member terms repeated", path: "/opportunities/7/member-terms?page=1&page=2", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.ListMemberTerms},
		{name: "history unknown", path: "/opportunities/7/stage-history?sort=asc", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.StageHistory},
		{name: "followups repeated", path: "/opportunities/7/followups?page_size=20&page_size=50", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.ListFollowups},
		{name: "malformed query escape", path: "/opportunities?keyword=x", rawQuery: "keyword=x&bad=%zz", call: handler.List},
		{name: "malformed query separator", path: "/opportunities?keyword=x", rawQuery: "keyword=x;bad=1", call: handler.List},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Params = test.params
			ginContext.Request = httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.rawQuery != "" {
				ginContext.Request.URL.RawQuery = test.rawQuery
			}
			test.call(ginContext)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "CRM_OPPORTUNITY_QUERY_INVALID") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestReadHandlersRejectInvalidTypedQueryValuesBeforeRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	principalContext := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	handler := &Handler{service: &Service{}}
	tests := []struct {
		name   string
		path   string
		params gin.Params
		call   gin.HandlerFunc
	}{
		{name: "list page", path: "/opportunities?page=0", call: handler.List},
		{name: "list page size", path: "/opportunities?page_size=101", call: handler.List},
		{name: "list stage", path: "/opportunities?stage=待审批", call: handler.List},
		{name: "list status", path: "/opportunities?status=ACTIVE", call: handler.List},
		{name: "list sort", path: "/opportunities?sort_by=name", call: handler.List},
		{name: "list sort order", path: "/opportunities?sort_order=random", call: handler.List},
		{name: "alert boolean", path: "/opportunity-stage-alerts?unread_only=1", call: handler.StageAlerts},
		{name: "member boolean", path: "/opportunities/7/members?include_inactive=yes", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.GetMembers},
		{name: "member term boolean", path: "/opportunities/7/member-terms?active_only=yes", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.ListMemberTerms},
		{name: "member term page", path: "/opportunities/7/member-terms?page=0", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.ListMemberTerms},
		{name: "history pagination", path: "/opportunities/7/stage-history?page=1000001", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.StageHistory},
		{name: "followup pagination", path: "/opportunities/7/followups?page_size=x", params: gin.Params{{Key: "id", Value: "7"}}, call: handler.ListFollowups},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Params = test.params
			ginContext.Request = httptest.NewRequest(http.MethodGet, test.path, nil).WithContext(principalContext)
			test.call(ginContext)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "CRM_OPPORTUNITY_QUERY_INVALID") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestOpportunityWriteHandlersRejectQueryBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	tests := []struct {
		name string
		path string
		call gin.HandlerFunc
	}{
		{name: "create", path: "/opportunities?dry_run=true", call: handler.Create},
		{name: "update", path: "/opportunities/7?force=true", call: handler.Update},
		{name: "external callback", path: "/integrations/qb/status-events?tenant=x", call: handler.ApplyExternalStatus},
		{name: "contract transfer", path: "/opportunities/7/contract-transfer?retry=true", call: handler.ContractTransfer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Params = gin.Params{{Key: "id", Value: "7"}}
			ginContext.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`))
			test.call(ginContext)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "CRM_OPPORTUNITY_QUERY_INVALID") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestExternalEdgeHandlersRejectZeroID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, call := range []gin.HandlerFunc{(&Handler{}).ExternalStatus, (&Handler{}).ContractTransfer, (&Handler{}).LaunchQuotation, (&Handler{}).LaunchBid} {
		recorder := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(recorder)
		ginContext.Params = gin.Params{{Key: "id", Value: "0"}}
		ginContext.Request = httptest.NewRequest(http.MethodGet, "/opportunities/0", nil)
		call(ginContext)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_INVALID_ARGUMENT") {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestOpportunityQueryValidationNormalizesLegalServiceInputs(t *testing.T) {
	query, err := validateListQuery(ListQuery{Keyword: " 商机 ", Stage: " 报价 ", Status: " following ", OwnerID: " sub-a ", Page: 1, PageSize: 20, SortBy: " EXPECTED_AMOUNT ", SortOrder: " ASC "})
	if err != nil {
		t.Fatalf("legal query rejected: %v", err)
	}
	if query.Keyword != "商机" || query.Stage != StageQuotation || query.Status != StatusFollowing || query.OwnerID != "sub-a" || query.SortBy != "expected_amount" || query.SortOrder != "asc" {
		t.Fatalf("query not normalized: %#v", query)
	}
	board, err := validateBoardQuery(ListQuery{Keyword: " 商机 ", OwnerID: " sub-a "})
	if err != nil || board.Keyword != "商机" || board.OwnerID != "sub-a" {
		t.Fatalf("board=%#v err=%v", board, err)
	}
}

func TestOpportunityServiceQueryValidationRejectsInvalidInputsBeforeRepository(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	service := &Service{}
	if _, err := service.List(ctx, ListQuery{Page: 0, PageSize: 20}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("list error=%v", err)
	}
	if _, err := service.Board(ctx, ListQuery{Stage: StageBid}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("board error=%v", err)
	}
	if _, err := service.StageHistory(ctx, 7, 1, 101); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("history error=%v", err)
	}
	if _, err := service.ListFollowups(ctx, 7, maxQueryPage+1, 20); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("followups error=%v", err)
	}
}
