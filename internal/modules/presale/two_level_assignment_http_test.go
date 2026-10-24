package presale

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func TestExecutorAssignmentRouteRequiresPermissionAndForwardsIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		permissions map[string]struct{}
		wantStatus  int
		wantExecute bool
	}{
		{name: "presale assign holder reaches the team lead service", permissions: map[string]struct{}{"presale.assign": {}}, wantStatus: http.StatusOK, wantExecute: true},
		{name: "read only principal is rejected at the route", permissions: map[string]struct{}{"presale.read": {}}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, instance := ts011Request(t, ts011TwoLevelNodes(t))
			request.Status = StatusPendingExecutionAssignment
			request.TeamLeadPersonID = "lead-a"
			request.TeamLeadNameSnapshot = "团队负责人甲"
			request.ExecutionDepartmentID = "org-1"
			request.AssignmentLevel = AssignmentLevelTeamLead
			request.Version = 4
			repo := &ts011Repository{
				mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
			}
			actor := ts011TeamLead("lead-a")
			service := newTS011Service(repo, ts011NewDirectory())
			handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: actor})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{
					TenantID: actor.TenantID, UserID: actor.UserID, Permissions: test.permissions,
				}))
				c.Next()
			})
			RegisterRoutes(router.Group("/api/v1"), handler)
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(http.MethodPut, "/api/v1/presale/requests/9/executor-assignments",
				strings.NewReader(`{"assignees":[{"person_id":"exec-a","role":"project_manager"}],"change_reason":"route test","version":4}`))
			httpRequest.Header.Set("Content-Type", "application/json")
			httpRequest.Header.Set("Idempotency-Key", "http-executor-key")
			router.ServeHTTP(recorder, httpRequest)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantExecute {
				if repo.request.Status != StatusExecuting || repo.replays["http-executor-key"] == nil {
					t.Fatalf("request status=%s replay=%+v", repo.request.Status, repo.replays["http-executor-key"])
				}
			} else if repo.request.Status != StatusPendingExecutionAssignment || len(repo.replays) != 0 {
				t.Fatalf("forbidden request changed state: status=%s replays=%d", repo.request.Status, len(repo.replays))
			}
		})
	}
}

func TestExecutingTakeoverRouteRequiresPermissionAndForwardsIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		permissions map[string]struct{}
		wantStatus  int
	}{
		{name: "assign holder reaches takeover", permissions: map[string]struct{}{"presale.assign": {}}, wantStatus: http.StatusOK},
		{name: "read only principal is rejected", permissions: map[string]struct{}{"presale.read": {}}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, instance := ts011Request(t, ts011TwoLevelNodes(t))
			request.Status, request.Version = StatusExecuting, 6
			request.ExecutionDepartmentID, request.ExecutionDepartment = "org-1", "交付一部"
			request.TeamLeadPersonID, request.TeamLeadNameSnapshot = "lead-a", "团队负责人甲"
			request.AssignmentLevel = AssignmentLevelExecutor
			repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}, assignments: []Assignment{
				{BaseModel: BaseModel{ID: 1, TenantID: "tenant-a", Version: 1}, RequestID: 9, AssigneeID: "lead-a", AssigneeRole: "team_lead", AssignmentKind: AssignmentKindTeamLead, IsCurrent: true, BatchNo: 1},
			}}}
			actor := ts011TechnicalDirector()
			service := newTS011Service(repo, ts011NewDirectory())
			handler := NewHandler(service, nil, fixedHandlerActorResolver{actor: actor})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{TenantID: actor.TenantID, UserID: actor.UserID, Permissions: test.permissions}))
				c.Next()
			})
			RegisterRoutes(router.Group("/api/v1"), handler)
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(http.MethodPut, "/api/v1/presale/requests/9/execution-takeover", strings.NewReader(`{"team_lead_person_id":"lead-b","reason":"原负责人失效","version":6}`))
			httpRequest.Header.Set("Content-Type", "application/json")
			httpRequest.Header.Set("Idempotency-Key", "http-takeover-key")
			router.ServeHTTP(recorder, httpRequest)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				if repo.request.Status != StatusExecuting || repo.request.TeamLeadPersonID != "lead-b" || repo.replays["http-takeover-key"] == nil {
					t.Fatalf("request status=%s lead=%s replay=%+v", repo.request.Status, repo.request.TeamLeadPersonID, repo.replays["http-takeover-key"])
				}
			} else if repo.request.TeamLeadPersonID != "lead-a" || len(repo.replays) != 0 {
				t.Fatalf("forbidden request changed state: lead=%s replays=%d", repo.request.TeamLeadPersonID, len(repo.replays))
			}
		})
	}
}
