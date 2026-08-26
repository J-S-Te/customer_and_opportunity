package portalbootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/feedback"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type feedbackCloseRouteRepository struct {
	value         *feedback.Feedback
	createCalls   int
	listCustomer  int
	listOperator  int
	findOperator  int
	findCustomer  int
	findForUpdate int
	findReplay    int
	updates       int
	lastListQuery feedback.ListQuery
	statusLogs    []feedback.StatusLog
	outbox        []feedback.Outbox
}

func (r *feedbackCloseRouteRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *feedbackCloseRouteRepository) Create(context.Context, *feedback.Feedback) error {
	r.createCalls++
	return errors.New("not used")
}
func (*feedbackCloseRouteRepository) FindByCreateKey(context.Context, feedback.CustomerActor, string) (*feedback.Feedback, error) {
	return nil, errors.New("not used")
}
func (r *feedbackCloseRouteRepository) ListCustomer(_ context.Context, _ feedback.CustomerActor, query feedback.ListQuery) (pagination.Page[feedback.Feedback], error) {
	r.listCustomer++
	r.lastListQuery = query
	return pagination.Page[feedback.Feedback]{Items: []feedback.Feedback{}, Page: query.Page, PageSize: query.PageSize}, nil
}
func (r *feedbackCloseRouteRepository) FindCustomer(_ context.Context, actor feedback.CustomerActor, id string) (*feedback.Feedback, error) {
	r.findCustomer++
	if r.value == nil || r.value.TenantID != actor.TenantID || r.value.CustomerID != actor.CustomerID || r.value.AccountID != actor.AccountID || r.value.PublicID != id {
		return nil, feedback.ErrNotFound
	}
	return r.value, nil
}
func (r *feedbackCloseRouteRepository) FindOperator(_ context.Context, tenant, id string) (*feedback.Feedback, error) {
	r.findOperator++
	if r.value == nil || r.value.TenantID != tenant || r.value.PublicID != id {
		return nil, feedback.ErrNotFound
	}
	return r.value, nil
}
func (r *feedbackCloseRouteRepository) ListOperator(_ context.Context, _ string, query feedback.ListQuery) (pagination.Page[feedback.Feedback], error) {
	r.listOperator++
	r.lastListQuery = query
	return pagination.Page[feedback.Feedback]{Items: []feedback.Feedback{}, Page: query.Page, PageSize: query.PageSize}, nil
}
func (r *feedbackCloseRouteRepository) FindForUpdate(_ context.Context, tenant string, id uint64) (*feedback.Feedback, error) {
	r.findForUpdate++
	if r.value == nil || r.value.TenantID != tenant || r.value.ID != id {
		return nil, feedback.ErrNotFound
	}
	return r.value, nil
}
func (r *feedbackCloseRouteRepository) Update(_ context.Context, value *feedback.Feedback, version uint64, fields map[string]any) error {
	r.updates++
	if value.Version != version {
		return errors.New("version conflict")
	}
	value.Status = fields["status"].(feedback.Status)
	value.Version++
	return nil
}
func (*feedbackCloseRouteRepository) CreateMessage(context.Context, *feedback.Message) error {
	return errors.New("not used")
}
func (*feedbackCloseRouteRepository) FindMessageByKey(context.Context, string, uint64, string, string, string) (*feedback.Message, error) {
	return nil, errors.New("not used")
}
func (*feedbackCloseRouteRepository) ListCustomerMessages(context.Context, string, uint64) ([]feedback.Message, error) {
	return []feedback.Message{}, nil
}
func (r *feedbackCloseRouteRepository) ListStatusLogs(context.Context, string, uint64) ([]feedback.StatusLog, error) {
	return r.statusLogs, nil
}
func (r *feedbackCloseRouteRepository) FindStatusActionByKey(_ context.Context, tenant, key string) (*feedback.StatusLog, error) {
	r.findReplay++
	for i := range r.statusLogs {
		item := &r.statusLogs[i]
		if item.TenantID == tenant && item.IdempotencyKey != nil && *item.IdempotencyKey == key {
			return item, nil
		}
	}
	return nil, feedback.ErrNotFound
}
func (r *feedbackCloseRouteRepository) CreateStatusLog(_ context.Context, value *feedback.StatusLog) error {
	r.statusLogs = append(r.statusLogs, *value)
	return nil
}
func (r *feedbackCloseRouteRepository) CreateOutbox(_ context.Context, value *feedback.Outbox) error {
	r.outbox = append(r.outbox, *value)
	return nil
}

func feedbackCloseRouteFixture(t *testing.T, permissions []string, repository *feedbackCloseRouteRepository) http.Handler {
	t.Helper()
	now := time.Date(2026, 8, 2, 4, 0, 0, 0, time.UTC)
	session := &account.Session{
		Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: reportNotificationRouteSessionHash("opaque-session"),
		PlatformUserID: "subject-a", CustomerID: 7, Permissions: permissions,
		AuthorizationCheckedAt: now, ExpiresAt: now.Add(time.Hour), AbsoluteExpiry: now.Add(time.Hour), LastSeenAt: now,
	}
	accountRepository := &reportNotificationRouteAccountRepository{
		session: session,
		link:    &account.IdentityLink{Model: database.Model{TenantID: "tenant-a"}, PlatformUserID: "subject-a", CustomerID: 7, Status: account.IdentityActive},
	}
	accountService := account.NewService(accountRepository, nil, nil, nil, reportNotificationRouteClock{now: now}, nil, "", time.Hour)
	feedbackService := feedbackCloseRouteService(t, repository)
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	return NewRouter(RouterDependencies{Config: config, Account: accountService, Feedback: feedbackService})
}

func feedbackCloseRouteService(t *testing.T, repository *feedbackCloseRouteRepository) *feedback.Service {
	t.Helper()
	now := time.Date(2026, 8, 2, 4, 0, 0, 0, time.UTC)
	return feedback.NewService(repository, nil, nil, reportNotificationRouteClock{now: now}, &feedbackCloseRouteIDs{})
}

type feedbackCloseRouteIDs struct{ n int }

func (i *feedbackCloseRouteIDs) NewID() string {
	i.n++
	return "feedback-close-route-event"
}

func serveFeedbackCloseRoute(handler http.Handler, origin, csrf, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/api/v1/feedbacks/feedback-1/close", nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestFeedbackCloseRouteRejectsPermissionAndCSRFFailureBeforeService(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
		origin      string
		csrf        string
	}{
		{name: "missing permission", permissions: []string{"feedback.read"}, origin: "https://portal.example", csrf: "1"},
		{name: "cross origin", permissions: []string{"feedback.reply"}, origin: "https://evil.example", csrf: "1"},
		{name: "missing csrf", permissions: []string{"feedback.reply"}, origin: "https://portal.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &feedbackCloseRouteRepository{}
			response := serveFeedbackCloseRoute(feedbackCloseRouteFixture(t, test.permissions, repository), test.origin, test.csrf, "close-key-1")
			if response.Code != http.StatusForbidden || repository.findCustomer != 0 || repository.findReplay != 0 || repository.updates != 0 {
				t.Fatalf("status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
			}
		})
	}
}

func TestFeedbackCloseRouteScopesResourceBeforeReplayAndRequiresKey(t *testing.T) {
	value := &feedback.Feedback{ActorModel: feedback.ActorModel{ID: 1, TenantID: "tenant-a", Version: 1}, PublicID: "feedback-1", CustomerID: 7, AccountID: "another-subject", Status: feedback.StatusResolved}
	repository := &feedbackCloseRouteRepository{value: value}
	handler := feedbackCloseRouteFixture(t, []string{"feedback.reply"}, repository)
	response := serveFeedbackCloseRoute(handler, "https://portal.example", "1", "close-key-1")
	if response.Code != http.StatusNotFound || repository.findCustomer != 1 || repository.findReplay != 0 || repository.updates != 0 {
		t.Fatalf("IDOR status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
	}

	repository.value.AccountID = "subject-a"
	response = serveFeedbackCloseRoute(handler, "https://portal.example", "1", "")
	if response.Code != http.StatusUnprocessableEntity || repository.findCustomer != 1 || repository.findReplay != 0 || repository.updates != 0 {
		t.Fatalf("missing-key status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
	}
}

func TestFeedbackCloseRoutePersistsAndReplaysOneStatusChange(t *testing.T) {
	value := &feedback.Feedback{ActorModel: feedback.ActorModel{ID: 1, TenantID: "tenant-a", Version: 1}, PublicID: "feedback-1", CustomerID: 7, AccountID: "subject-a", Status: feedback.StatusResolved}
	repository := &feedbackCloseRouteRepository{value: value}
	handler := feedbackCloseRouteFixture(t, []string{"feedback.reply"}, repository)
	for attempt := 0; attempt < 2; attempt++ {
		response := serveFeedbackCloseRoute(handler, "https://portal.example", "1", "close-key-1")
		if response.Code != http.StatusOK {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if value.Status != feedback.StatusClosed || repository.updates != 1 || len(repository.statusLogs) != 1 || len(repository.outbox) != 1 {
		t.Fatalf("value=%#v repository=%#v", value, repository)
	}
}
