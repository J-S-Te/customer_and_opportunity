package portalbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type reportNotificationRouteAccountRepository struct {
	session *account.Session
	link    *account.IdentityLink
}

func (r *reportNotificationRouteAccountRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (*reportNotificationRouteAccountRepository) UpsertPendingLink(context.Context, *account.IdentityLink) (*account.IdentityLink, error) {
	return nil, errors.New("not used")
}
func (r *reportNotificationRouteAccountRepository) FindLink(_ context.Context, tenantID, subject string) (*account.IdentityLink, error) {
	if r.link == nil || r.link.TenantID != tenantID || r.link.PlatformUserID != subject {
		return nil, account.ErrNotProvisioned
	}
	return r.link, nil
}
func (*reportNotificationRouteAccountRepository) ActivateLink(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) RevertActivation(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) CreateActivation(context.Context, *account.ActivationContext) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) ConsumeActivation(context.Context, string, time.Time) (*account.ActivationContext, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) CreateSession(context.Context, *account.Session) error {
	return errors.New("not used")
}
func (r *reportNotificationRouteAccountRepository) FindSession(_ context.Context, tenantID, sessionHash string, _ time.Time) (*account.Session, error) {
	if r.session == nil || r.session.TenantID != tenantID || r.session.SessionIDHash != sessionHash {
		return nil, account.ErrInvalidLoginState
	}
	return r.session, nil
}
func (*reportNotificationRouteAccountRepository) ListSessions(context.Context, string, string, time.Time) ([]account.Session, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) FindOwnedSession(context.Context, string, string, string, time.Time) (*account.Session, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) RevokeSession(context.Context, string, string, string, time.Time) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) RevokeSessionsForSubject(context.Context, string, string, time.Time) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) TouchSession(context.Context, string, string, time.Time, time.Time) error {
	return nil
}
func (*reportNotificationRouteAccountRepository) MarkLinkVerified(context.Context, string, uint64, uint64, time.Time) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) WriteAuthEvent(context.Context, *account.AuthEvent) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) CreateSecurityEvent(context.Context, *account.SecurityEvent) error {
	return errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) ListSecurityEvents(context.Context, string, string, int) ([]account.SecurityEvent, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteAccountRepository) AcknowledgeSecurityEvent(context.Context, string, string, string, time.Time) error {
	return errors.New("not used")
}

type reportNotificationRouteClock struct{ now time.Time }

func (c reportNotificationRouteClock) Now() time.Time { return c.now }

type reportNotificationRouteRepository struct {
	notification  *report.Notification
	listCalls     int
	countCalls    int
	findCalls     int
	markCalls     int
	readEvents    []report.NotificationReadEvent
	listActor     report.Actor
	listUnread    bool
	listPage      int
	listPageSize  int
	lastFindActor report.Actor
}

func (r *reportNotificationRouteRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (*reportNotificationRouteRepository) Create(context.Context, *report.Request) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) List(context.Context, string, uint64, int, int) (pagination.Page[report.Request], error) {
	return pagination.Page[report.Request]{}, errors.New("not used")
}
func (*reportNotificationRouteRepository) Find(context.Context, string, uint64, uint64) (*report.Request, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteRepository) FindByIdempotencyKey(context.Context, string, uint64, string) (*report.Request, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteRepository) FindForUpdate(context.Context, string, uint64) (*report.Request, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteRepository) Update(context.Context, *report.Request, uint64, map[string]any) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) CreateFile(context.Context, *report.File) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) CreateIngestJob(context.Context, *report.IngestJob) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) FindIngestJobForUpdate(context.Context, uint64) (*report.IngestJob, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteRepository) UpdateIngestJob(context.Context, *report.IngestJob, map[string]any) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) CreateOutbox(context.Context, *report.Outbox) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) CreateStatusEvent(context.Context, *report.StatusEvent) error {
	return errors.New("not used")
}
func (*reportNotificationRouteRepository) FindStatusEventBySource(context.Context, string, uint64, string) (*report.StatusEvent, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteRepository) ListStatusEvents(context.Context, string, uint64, uint64) ([]report.StatusEvent, error) {
	return nil, errors.New("not used")
}
func (*reportNotificationRouteRepository) CreateNotification(context.Context, *report.Notification) error {
	return errors.New("not used")
}
func (r *reportNotificationRouteRepository) ListNotifications(_ context.Context, actor report.Actor, unreadOnly bool, page, pageSize int) (pagination.Page[report.NotificationView], error) {
	r.listCalls++
	r.listActor, r.listUnread, r.listPage, r.listPageSize = actor, unreadOnly, page, pageSize
	return pagination.Page[report.NotificationView]{Items: []report.NotificationView{}, Page: page, PageSize: pageSize}, nil
}
func (r *reportNotificationRouteRepository) CountUnreadNotifications(_ context.Context, actor report.Actor) (int64, error) {
	r.countCalls++
	r.lastFindActor = actor
	return 0, nil
}
func (r *reportNotificationRouteRepository) FindNotificationForUpdate(_ context.Context, actor report.Actor, id uint64) (*report.Notification, error) {
	r.findCalls++
	r.lastFindActor = actor
	if r.notification == nil || r.notification.ID != id || r.notification.TenantID != actor.TenantID || r.notification.CustomerID != actor.CustomerID || r.notification.AccountID != actor.AccountID {
		return nil, report.ErrNotificationNotFound
	}
	return r.notification, nil
}
func (r *reportNotificationRouteRepository) MarkNotificationRead(_ context.Context, value *report.Notification, at time.Time) error {
	r.markCalls++
	value.Status, value.ReadAt = report.NotificationRead, &at
	return nil
}
func (r *reportNotificationRouteRepository) CreateNotificationReadEvent(_ context.Context, value *report.NotificationReadEvent) error {
	r.readEvents = append(r.readEvents, *value)
	return nil
}

func reportNotificationRouteFixture(t *testing.T, permissions []string, repository *reportNotificationRouteRepository) (*ginRoute, *reportNotificationRouteRepository) {
	t.Helper()
	now := time.Date(2026, 8, 1, 3, 4, 5, 0, time.UTC)
	session := &account.Session{
		Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: reportNotificationRouteSessionHash("opaque-session"),
		PlatformUserID: "subject-a", CustomerID: 7, Permissions: permissions,
		AuthorizationCheckedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiry: now.Add(time.Hour),
	}
	accountRepository := &reportNotificationRouteAccountRepository{
		session: session,
		link:    &account.IdentityLink{Model: database.Model{TenantID: "tenant-a"}, PlatformUserID: "subject-a", CustomerID: 7, Status: account.IdentityActive},
	}
	accountService := account.NewService(accountRepository, nil, nil, nil, reportNotificationRouteClock{now: now}, nil, "", time.Hour)
	if repository == nil {
		repository = &reportNotificationRouteRepository{}
	}
	reportService := report.NewService(repository, nil, nil, nil, reportNotificationRouteClock{now: now}, nil)
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	return &ginRoute{handler: NewRouter(RouterDependencies{Config: config, Account: accountService, Reports: reportService})}, repository
}

func reportNotificationRouteSessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ginRoute keeps request construction below focused on the public HTTP contract.
type ginRoute struct{ handler http.Handler }

func (r *ginRoute) serve(method, target, origin, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, request)
	return response
}

func TestReportNotificationRoutesRejectMissingReadPermissionBeforeDataAccess(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/customer-portal/api/v1/report-notifications"},
		{method: http.MethodGet, path: "/customer-portal/api/v1/report-notifications/unread-count"},
		{method: http.MethodPost, path: "/customer-portal/api/v1/report-notifications/9/read"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			route, repository := reportNotificationRouteFixture(t, []string{"project.read"}, nil)
			response := route.serve(test.method, test.path, "https://portal.example", "1")
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusForbidden, response.Body.String())
			}
			if repository.listCalls != 0 || repository.countCalls != 0 || repository.findCalls != 0 || repository.markCalls != 0 {
				t.Fatalf("permission failure reached report repository: %#v", repository)
			}
		})
	}
}

func TestReadReportNotificationRequiresSameOriginAndCSRFThroughRealRoute(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		csrf   string
		status int
	}{
		{name: "missing origin", csrf: "1", status: http.StatusForbidden},
		{name: "cross origin", origin: "https://evil.example", csrf: "1", status: http.StatusForbidden},
		{name: "missing csrf", origin: "https://portal.example", status: http.StatusForbidden},
		{name: "wrong csrf", origin: "https://portal.example", csrf: "0", status: http.StatusForbidden},
		{name: "same origin and csrf", origin: "https://portal.example", csrf: "1", status: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			notification := &report.Notification{ID: 9, TenantID: "tenant-a", CustomerID: 7, AccountID: "subject-a", RequestID: 12, Status: report.NotificationUnread}
			route, repository := reportNotificationRouteFixture(t, []string{"report.read"}, &reportNotificationRouteRepository{notification: notification})
			response := route.serve(http.MethodPost, "/customer-portal/api/v1/report-notifications/9/read", test.origin, test.csrf)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusForbidden && (repository.findCalls != 0 || repository.markCalls != 0 || len(repository.readEvents) != 0 || notification.Status != report.NotificationUnread) {
				t.Fatalf("CSRF failure changed notification state: repository=%#v notification=%#v", repository, notification)
			}
			if test.status == http.StatusOK && (repository.findCalls != 1 || repository.markCalls != 1 || len(repository.readEvents) != 1 || notification.Status != report.NotificationRead) {
				t.Fatalf("valid read did not traverse service transaction: repository=%#v notification=%#v", repository, notification)
			}
		})
	}
}

func TestReadReportNotificationRejectsNonPositiveOrMalformedIDBeforeDataAccess(t *testing.T) {
	for _, id := range []string{"0", "-1", "abc", "1.5", "18446744073709551616"} {
		t.Run(id, func(t *testing.T) {
			route, repository := reportNotificationRouteFixture(t, []string{"report.read"}, nil)
			response := route.serve(http.MethodPost, "/customer-portal/api/v1/report-notifications/"+id+"/read", "https://portal.example", "1")
			if response.Code != http.StatusBadRequest || !responseHasCode(t, response, "COMMON_VALIDATION_ERROR") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if repository.findCalls != 0 || repository.markCalls != 0 || len(repository.readEvents) != 0 {
				t.Fatalf("invalid id reached report repository: %#v", repository)
			}
		})
	}
}

func TestReportNotificationListStrictlyValidatesQueryAndPagination(t *testing.T) {
	invalidQueries := []string{
		"page=0", "page=not-a-number", "page_size=0", "page_size=101",
		"page=1&page=2", "unknown=value", "unread_only=true&unread_only=false",
		"unread_only=not-a-bool", "page=9223372036854775807&page_size=100",
	}
	for _, query := range invalidQueries {
		t.Run(query, func(t *testing.T) {
			route, repository := reportNotificationRouteFixture(t, []string{"report.read"}, nil)
			response := route.serve(http.MethodGet, "/customer-portal/api/v1/report-notifications?"+query, "", "")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if repository.listCalls != 0 {
				t.Fatalf("invalid query reached report repository: %#v", repository)
			}
		})
	}

	route, repository := reportNotificationRouteFixture(t, []string{"report.read"}, nil)
	response := route.serve(http.MethodGet, "/customer-portal/api/v1/report-notifications?page=2&page_size=3&unread_only=true", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	wantActor := report.Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "subject-a"}
	if repository.listCalls != 1 || repository.listActor != wantActor || !repository.listUnread || repository.listPage != 2 || repository.listPageSize != 3 {
		t.Fatalf("valid list did not preserve authenticated scope and pagination: %#v", repository)
	}
}

func TestReportNotificationUnreadCountRejectsUnexpectedQueryBeforeDataAccess(t *testing.T) {
	route, repository := reportNotificationRouteFixture(t, []string{"report.read"}, nil)
	response := route.serve(http.MethodGet, "/customer-portal/api/v1/report-notifications/unread-count?account_id=subject-b", "", "")
	if response.Code != http.StatusBadRequest || !responseHasCode(t, response, "COMMON_INVALID_ARGUMENT") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.countCalls != 0 {
		t.Fatalf("unexpected query reached report repository: %#v", repository)
	}
}

func TestReadReportNotificationCrossAccountIsIndistinguishableFromMissingAndCannotMutate(t *testing.T) {
	otherAccountNotification := &report.Notification{ID: 9, TenantID: "tenant-a", CustomerID: 7, AccountID: "subject-b", RequestID: 12, Status: report.NotificationUnread}
	repository := &reportNotificationRouteRepository{notification: otherAccountNotification}
	route, repository := reportNotificationRouteFixture(t, []string{"report.read"}, repository)

	crossAccount := route.serve(http.MethodPost, "/customer-portal/api/v1/report-notifications/9/read", "https://portal.example", "1")
	missing := route.serve(http.MethodPost, "/customer-portal/api/v1/report-notifications/10/read", "https://portal.example", "1")
	if crossAccount.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("cross-account status=%d missing status=%d", crossAccount.Code, missing.Code)
	}
	if !responseHasCode(t, crossAccount, "PORTAL_REPORT_NOTIFICATION_NOT_FOUND") || !responseHasCode(t, missing, "PORTAL_REPORT_NOTIFICATION_NOT_FOUND") {
		t.Fatalf("cross-account and missing responses differ: cross=%s missing=%s", crossAccount.Body.String(), missing.Body.String())
	}
	if repository.lastFindActor != (report.Actor{TenantID: "tenant-a", CustomerID: 7, AccountID: "subject-a"}) {
		t.Fatalf("repository lookup did not use authenticated account scope: %#v", repository.lastFindActor)
	}
	if repository.markCalls != 0 || len(repository.readEvents) != 0 || otherAccountNotification.Status != report.NotificationUnread || otherAccountNotification.ReadAt != nil {
		t.Fatalf("cross-account read changed state: repository=%#v notification=%#v", repository, otherAccountNotification)
	}
}

func responseHasCode(t *testing.T, response *httptest.ResponseRecorder, want string) bool {
	t.Helper()
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid response envelope: %v body=%s", err, response.Body.String())
	}
	return envelope.Code == want
}
