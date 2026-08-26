package portalbootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type reportRiskRouteRepo struct {
	reportNotificationRouteRepository
	operatorLists int
	status        string
	actor         report.Actor
}

func (*reportRiskRouteRepo) CreateRiskAlert(context.Context, *report.RiskAlert) error { return nil }
func (r *reportRiskRouteRepo) ListRiskAlerts(_ context.Context, actor report.Actor, openOnly bool, page, pageSize int) (pagination.Page[report.RiskAlertView], error) {
	r.actor = actor
	return pagination.Page[report.RiskAlertView]{Items: []report.RiskAlertView{}, Page: page, PageSize: pageSize}, nil
}
func (r *reportRiskRouteRepo) ListRiskAlertsForReview(_ context.Context, tenantID, status string, page, pageSize int) (pagination.Page[report.RiskAlertView], error) {
	r.operatorLists++
	r.status = tenantID + ":" + status
	return pagination.Page[report.RiskAlertView]{Items: []report.RiskAlertView{}, Page: page, PageSize: pageSize}, nil
}
func (*reportRiskRouteRepo) FindRiskAlertForUpdate(context.Context, string, string) (*report.RiskAlert, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) FindRiskAlertView(context.Context, string, string) (*report.RiskAlertView, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) UpdateRiskAlert(context.Context, *report.RiskAlert, map[string]any) error {
	return errors.New("not used")
}
func (*reportRiskRouteRepo) CreateRiskReviewEvent(context.Context, *report.RiskReviewEvent) error {
	return errors.New("not used")
}
func (*reportRiskRouteRepo) FindRiskReviewEvent(context.Context, string, string, string) (*report.RiskReviewEvent, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) FindGrantByIDForUpdate(context.Context, string, uint64) (*report.Grant, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) FindActiveGrantForUpdate(context.Context, string, uint64, uint64, string) (*report.Grant, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) Find(context.Context, string, uint64, uint64) (*report.Request, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) FindFile(context.Context, string, uint64) (*report.File, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) RevokeActiveGrants(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*reportRiskRouteRepo) CreateGrant(context.Context, *report.Grant) error {
	return errors.New("not used")
}
func (*reportRiskRouteRepo) FindGrantByIssueKeyForUpdate(context.Context, string, uint64, uint64, string, string) (*report.Grant, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) FindGrantForUpdate(context.Context, string, uint64, uint64, string, string) (*report.Grant, error) {
	return nil, errors.New("not used")
}
func (*reportRiskRouteRepo) UpdateGrant(context.Context, *report.Grant, map[string]any) error {
	return errors.New("not used")
}
func (*reportRiskRouteRepo) CreateDownloadEvent(context.Context, *report.DownloadEvent) error {
	return errors.New("not used")
}
func (*reportRiskRouteRepo) CreateDownloadEventOnce(context.Context, *report.DownloadEvent) error {
	return errors.New("not used")
}

func TestOwnedRiskAlertsRequireReportReadAndUseSessionScope(t *testing.T) {
	for _, test := range []struct {
		permissions []string
		status      int
		calls       bool
	}{{[]string{"project.read"}, http.StatusForbidden, false}, {[]string{"report.read"}, http.StatusOK, true}} {
		repository := &reportRiskRouteRepo{}
		route, _ := reportNotificationRouteFixture(t, test.permissions, &repository.reportNotificationRouteRepository)
		downloads := report.NewDownloadService(repository, nil, nil, reportNotificationRouteClock{now: time.Now().UTC()}, nil, nil, 0)
		route.handler = NewRouter(RouterDependencies{Config: Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}, Account: reportRiskRouteAccountService(t, test.permissions), ReportDownloads: downloads})
		response := route.serve(http.MethodGet, "/customer-portal/api/v1/report-risk-alerts?open_only=true", "", "")
		if response.Code != test.status {
			t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
		}
		if test.calls && repository.actor != (report.Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "subject-a"}) {
			t.Fatalf("actor=%+v", repository.actor)
		}
		if !test.calls && repository.actor.TenantID != "" {
			t.Fatal("permission failure reached repository")
		}
	}
}

func TestInternalRiskReviewRoutesRequireExactMachineScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		scope  string
		status int
		calls  int
	}{{"report.callback.write", http.StatusForbidden, 0}, {"portal.report.risk.manage", http.StatusOK, 1}} {
		repository := &reportRiskRouteRepo{}
		downloads := report.NewDownloadService(repository, nil, nil, reportNotificationRouteClock{now: time.Now().UTC()}, nil, nil, 0)
		router := NewRouter(RouterDependencies{
			Config:               Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"},
			ReportDownloads:      downloads,
			MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{UserID: "machine:reviewer", TenantID: "tenant-a", Permissions: map[string]struct{}{test.scope: {}}}},
		})
		request := httptest.NewRequest(http.MethodGet, "/customer-portal/internal/report-risk-alerts?status=OPEN", nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status || repository.operatorLists != test.calls {
			t.Fatalf("scope=%s status=%d calls=%d body=%s", test.scope, response.Code, repository.operatorLists, response.Body.String())
		}
		if test.calls == 1 && repository.status != "tenant-a:OPEN" {
			t.Fatalf("operator list scope=%q", repository.status)
		}
	}
}

func reportRiskRouteAccountService(t *testing.T, permissions []string) *account.Service {
	t.Helper()
	now := time.Now().UTC()
	repository := &reportNotificationRouteAccountRepository{
		session: &account.Session{Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: reportNotificationRouteSessionHash("opaque-session"), PlatformUserID: "subject-a", CustomerID: 7, Permissions: permissions, AuthorizationCheckedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiry: now.Add(time.Hour), LastSeenAt: now},
		link:    &account.IdentityLink{Model: database.Model{TenantID: "tenant-a"}, PlatformUserID: "subject-a", CustomerID: 7, Status: account.IdentityActive},
	}
	return account.NewService(repository, nil, nil, nil, reportNotificationRouteClock{now: now}, nil, "", time.Hour)
}
