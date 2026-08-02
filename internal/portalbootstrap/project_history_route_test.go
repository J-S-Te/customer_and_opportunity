package portalbootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

func TestProjectHistoryInternalRouteUsesMachineTenantAndMinimumDTO(t *testing.T) {
	now := time.Now().UTC()
	lastSuccess := now.Add(-30 * time.Second)
	repo := &projectHistoryRouteRepository{lastSuccessAt: &lastSuccess, page: pagination.Page[project.Snapshot]{Items: []project.Snapshot{{
		ProjectID: "P-1", ProjectName: "项目", ContractNo: "HT-1", Status: "EXECUTING", ProgressPct: 60,
		CurrentStage: "实施", ManagerName: "经理", ManagerContactMasked: "138****0000", ManagerPortalAccountID: "private-account",
		SourceUpdatedAt: now.Add(-time.Minute), SyncedAt: now.Add(-time.Minute), RawVersion: "private-version",
	}}, Page: 1, PageSize: 20, Total: 1}}
	principal := sharedauth.Principal{TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.project_history.read": {}}}
	router := NewRouter(RouterDependencies{
		Config:   Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", ProjectHistoryStaleAfter: 10 * time.Minute},
		Projects: project.NewService(repo, nil), MachineAuthenticator: fakeMachineAuthenticator{principal: principal},
	})
	request := httptest.NewRequest(http.MethodGet, "/customer-portal/internal/customers/7/projects?page=1&page_size=20", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.scope.TenantID != "tenant-a" || repo.scope.CustomerID != 7 {
		t.Fatalf("status=%d scope=%#v body=%s", response.Code, repo.scope, response.Body.String())
	}
	for _, forbidden := range []string{"private-account", "private-version", "138****0000", "manager_portal_account_id", "manager_contact", "raw_version", `"tenant_id"`, `"customer_id"`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("forbidden field %q leaked: %s", forbidden, response.Body.String())
		}
	}
	for _, required := range []string{`"project_id":"P-1"`, `"source_updated_at"`, `"synced_at"`, `"sync_last_success_at"`, `"stale"`, `"staleness_seconds"`, `"request_id"`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("required field %q missing: %s", required, response.Body.String())
		}
	}
}

func TestProjectHistoryInternalRouteRejectsScopeAndQueryExpansion(t *testing.T) {
	tests := []struct {
		name, path, scope string
		status            int
	}{
		{name: "wrong scope", path: "/customer-portal/internal/customers/7/projects", scope: "portal.feedback.manage", status: http.StatusForbidden},
		{name: "over scoped", path: "/customer-portal/internal/customers/7/projects", scope: "over", status: http.StatusForbidden},
		{name: "invalid customer", path: "/customer-portal/internal/customers/0/projects", scope: "portal.project_history.read", status: http.StatusBadRequest},
		{name: "unknown tenant query", path: "/customer-portal/internal/customers/7/projects?tenant_id=other", scope: "portal.project_history.read", status: http.StatusBadRequest},
		{name: "duplicate page", path: "/customer-portal/internal/customers/7/projects?page=1&page=2", scope: "portal.project_history.read", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			permissions := map[string]struct{}{test.scope: {}}
			if test.scope == "over" {
				permissions = map[string]struct{}{"portal.project_history.read": {}, "portal.feedback.manage": {}}
			}
			router := NewRouter(RouterDependencies{Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}, MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{TenantID: "tenant-a", Permissions: permissions}}})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

type projectHistoryRouteRepository struct {
	project.Repository
	page          pagination.Page[project.Snapshot]
	scope         project.Scope
	lastSuccessAt *time.Time
}

func (r *projectHistoryRouteRepository) LastSuccessfulSync(_ context.Context, scope project.Scope) (*time.Time, error) {
	r.scope = scope
	return r.lastSuccessAt, nil
}

func (r *projectHistoryRouteRepository) List(_ context.Context, scope project.Scope, _ project.ListQuery) (pagination.Page[project.Snapshot], error) {
	r.scope = scope
	return r.page, nil
}
