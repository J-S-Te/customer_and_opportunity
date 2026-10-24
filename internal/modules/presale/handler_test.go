package presale

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type handlerActorResolver struct{}

func (handlerActorResolver) Resolve(context.Context) (Actor, error) {
	return Actor{TenantID: "tenant-a", UserID: "user-a"}, nil
}

type fixedHandlerActorResolver struct{ actor Actor }

func (r fixedHandlerActorResolver) Resolve(context.Context) (Actor, error) { return r.actor, nil }

func TestReportExportHTTPDownloadAndAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scope := range []string{"ALL", "ORG", "SELF"} {
		t.Run(scope, func(t *testing.T) {
			handler := NewHandler(nil, nil, fixedHandlerActorResolver{actor: reportActor(scope)}).UseReports(NewReportService(&reportRepositoryStub{}))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/presale/reports/exports", strings.NewReader(`{"from":"2026-08-01T00:00:00Z","to":"2026-08-03T00:00:00Z","dimension":"PERSON"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			handler.RequestReportExport(c)
			if recorder.Code != 200 || recorder.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") || recorder.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
			reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(recorder.Body.String(), "\xef\xbb\xbf")))
			reader.FieldsPerRecord = -1
			if _, err := reader.ReadAll(); err != nil {
				t.Fatal(err)
			}
		})
	}
	actor := reportActor("ALL")
	actor.Permissions = nil
	handler := NewHandler(nil, nil, fixedHandlerActorResolver{actor: actor}).UseReports(NewReportService(&reportRepositoryStub{}))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/presale/reports/exports", strings.NewReader(`{"from":"2026-08-01T00:00:00Z","to":"2026-08-03T00:00:00Z"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.RequestReportExport(c)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestJSONWriteHandlerRejectsUnsafeBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := map[string]string{
		"unknown field":    `{"opportunity_id":1,"venue":"现场","service_address":"address","contact_name":"name","contact_phone":"13800138000","description":"long enough description","expected_start":"2026-08-01T01:00:00Z","expected_end":"2026-08-01T02:00:00Z","urgency":"普通","unexpected":"value"}`,
		"trailing object":  `{}` + `{}`,
		"empty body":       "",
		"missing required": `{}`,
		"over one MiB":     `{"description":"` + strings.Repeat("x", (1<<20)+1) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/presale/requests", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			ginContext.Request = request

			(&Handler{actors: handlerActorResolver{}}).CreateRequest(ginContext)

			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"COMMON_INVALID_ARGUMENT"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestBindJSONAcceptsLegalFieldsAndRunsValidator(t *testing.T) {
	valid := `{"action":"PASS","version":1}`
	context := presaleJSONContext(valid)
	var input ApprovalActionInput
	if !bindJSON(context, &input) {
		t.Fatalf("valid request rejected: status=%d body=%s", context.Writer.Status(), context.Writer.Header().Get("Content-Type"))
	}
	if input.Action != "PASS" || input.Version != 1 {
		t.Fatalf("decoded input = %#v", input)
	}

	context = presaleJSONContext(`{"action":"PASS"}`)
	if bindJSON(context, &ApprovalActionInput{}) || context.Writer.Status() != http.StatusBadRequest {
		t.Fatalf("required validation did not reject request: status=%d", context.Writer.Status())
	}
}

func TestManualMutationHandlersForwardIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		actor   Actor
		request *PresaleRequest
		body    string
		invoke  func(*Handler, *gin.Context)
		setup   func(*mutationRepository)
	}{
		{
			name: "replace assignments", actor: Actor{TenantID: "tenant-a", UserID: "lead", Roles: map[string]bool{"team_lead": true}, Permissions: map[string]bool{"presale.assign": true}},
			request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, Status: StatusApprovedPendingAssignment},
			body:    `{"assignees":[{"person_id":"p1","role":"technician"}],"change_reason":"assign","version":3}`,
			invoke:  func(handler *Handler, ctx *gin.Context) { handler.ReplaceAssignments(ctx) },
			setup: func(repo *mutationRepository) {
				repo.engineers = []Engineer{{PersonID: "p1", PersonName: "P1", Role: "technician", ValidFlag: true}}
			},
		},
		{
			name: "cancel", actor: Actor{TenantID: "tenant-a", UserID: "sales"},
			request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, ApplicantID: "sales", Status: StatusPendingApproval},
			body:    `{"reason":"duplicate","version":3}`,
			invoke:  func(handler *Handler, ctx *gin.Context) { handler.Cancel(ctx) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &mutationRepository{request: test.request, replays: map[string]*MutationReplay{}}
			if test.setup != nil {
				test.setup(repo)
			}
			service := NewService(repo, nil, nil, fixedClock{at: now}, fixedIDs{}).UseOwnerDirectory(assignmentOwnerCatalog{
				users: map[string]ownerdirectory.User{
					"p1": {ID: "p1", DisplayName: "P1", Organizations: []ownerdirectory.Organization{{ID: "org-1", Name: "技术中心", IsPrimary: true}}},
				},
				roles: map[string]string{"p1": "technician"},
			})
			handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: test.actor})
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/presale/requests/9", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "handler-key")
			ctx.Request = request
			ctx.Params = gin.Params{{Key: "id", Value: "9"}}
			test.invoke(handler, ctx)
			if recorder.Code < 200 || recorder.Code >= 300 || repo.replays["handler-key"] == nil {
				t.Fatalf("status=%d body=%s replay=%+v", recorder.Code, recorder.Body.String(), repo.replays["handler-key"])
			}
		})
	}
}

func TestApprovalActionHTTPBoundaryUsesInternalApprovalWhenNoExternalResolver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &mutationRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval: &ApprovalInstance{EngineInstanceID: "crm-presale-9", Status: "PENDING", CurrentNode: 1},
		replays:  map[string]*MutationReplay{},
	}
	handler := NewHandler(NewService(repository, nil, nil, fixedClock{}, fixedIDs{}), nil, fixedHandlerActorResolver{actor: Actor{
		TenantID: "tenant-a", UserID: "director", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true},
	}})
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "9"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/presale/requests/9/approval-actions", strings.NewReader(`{"action":"PASS","version":3}`))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("Idempotency-Key", "approval-key")
	handler.ApprovalAction(context)
	if recorder.Code != http.StatusAccepted || repository.outboxCount != 0 || len(repository.replays) != 1 || len(repository.approvalLogs) != 1 {
		t.Fatalf("status=%d body=%s outbox=%d replays=%d", recorder.Code, recorder.Body.String(), repository.outboxCount, len(repository.replays))
	}
}

func TestApprovalActionHTTPBoundaryResolvesTaskServerSide(t *testing.T) {
	gin.SetMode(gin.TestMode)
	actor := Actor{TenantID: "tenant-a", UserID: "director", RequestID: "request-1", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	repository := &mutationRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval: &ApprovalInstance{EngineInstanceID: "instance-1", Status: "PENDING", CurrentNode: 1}, replays: map[string]*MutationReplay{},
	}
	service := NewService(repository, nil, nil, fixedClock{}, fixedIDs{}).UseApprovalTaskResolver(approvalResolver(actor))
	handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: actor})
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "9"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/presale/requests/9/approval-actions", strings.NewReader(`{"action":"PASS","version":3}`))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("Idempotency-Key", "approval-key")
	handler.ApprovalAction(context)
	if recorder.Code != http.StatusAccepted || repository.outboxCount != 1 || repository.replays["approval-key"] == nil {
		t.Fatalf("status=%d body=%s outbox=%d replay=%+v", recorder.Code, recorder.Body.String(), repository.outboxCount, repository.replays["approval-key"])
	}
}

func presaleJSONContext(body string) *gin.Context {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request
	return context
}

func TestOnlyQueryKeysRejectsUnknownAndDuplicateParameters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url     string
		allowed []string
		want    bool
	}{
		{url: "/timeline?limit=20&cursor=opaque", allowed: []string{"limit", "cursor"}, want: true},
		{url: "/timeline?limit=20&unknown=1", allowed: []string{"limit", "cursor"}, want: false},
		{url: "/timeline?limit=20&limit=30", allowed: []string{"limit", "cursor"}, want: false},
		{url: "/actions", want: true},
		{url: "/actions?scope=all", want: false},
		{url: "/actions?valid=1;scope=all", allowed: []string{"valid"}, want: false},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		contextValue, _ := gin.CreateTestContext(recorder)
		contextValue.Request = httptest.NewRequest(http.MethodGet, test.url, nil)
		if got := onlyQueryKeys(contextValue, test.allowed...); got != test.want {
			t.Errorf("onlyQueryKeys(%q)=%v, want %v", test.url, got, test.want)
		}
	}
}

func TestRequestQueryKeySetsFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url     string
		allowed []string
		want    bool
	}{
		{url: "/requests?page=1&page_size=20&status=EXECUTING", allowed: requestListQueryKeys, want: true},
		{url: "/requests?scope=all", allowed: requestListQueryKeys, want: false},
		{url: "/requests?status=EXECUTING&status=COMPLETED", allowed: requestListQueryKeys, want: false},
		{url: "/board?column_limit=20", allowed: append(requestFilterQueryKeys, "column_limit"), want: true},
		{url: "/filter-options?page=1", allowed: requestFilterQueryKeys, want: false},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		contextValue, _ := gin.CreateTestContext(recorder)
		contextValue.Request = httptest.NewRequest(http.MethodGet, test.url, nil)
		if got := onlyQueryKeys(contextValue, test.allowed...); got != test.want {
			t.Errorf("onlyQueryKeys(%q)=%v, want %v", test.url, got, test.want)
		}
	}
}
