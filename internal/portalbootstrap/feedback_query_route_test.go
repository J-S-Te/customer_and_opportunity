package portalbootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func serveFeedbackQueryRoute(handler http.Handler, method, target, body string, browserWrite bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if browserWrite {
		request.Header.Set("Origin", "https://portal.example")
		request.Header.Set("X-CSRF-Token", "1")
	}
	request.Header.Set("Idempotency-Key", "feedback-query-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestFeedbackCustomerListStrictlyBindsAllowedFiltersAndPagination(t *testing.T) {
	repository := &feedbackCloseRouteRepository{}
	handler := feedbackCloseRouteFixture(t, []string{"feedback.read"}, repository)

	response := serveFeedbackQueryRoute(handler, http.MethodGet, "/customer-portal/api/v1/feedbacks?status=RESOLVED&type=COMPLAINT&page=2&page_size=30", "", false)
	if response.Code != http.StatusOK || repository.listCustomer != 1 {
		t.Fatalf("status=%d list_calls=%d body=%s", response.Code, repository.listCustomer, response.Body.String())
	}
	query := repository.lastListQuery
	if query.Status != "RESOLVED" || query.Type != "COMPLAINT" || query.Page != 2 || query.PageSize != 30 {
		t.Fatalf("query=%#v", query)
	}
}

func TestFeedbackCustomerListRejectsMalformedQueryBeforeRepository(t *testing.T) {
	tests := []struct {
		name, query, code string
	}{
		{name: "unknown", query: "customer_id=7", code: "COMMON_INVALID_ARGUMENT"},
		{name: "duplicate filter", query: "status=RESOLVED&status=CLOSED", code: "COMMON_INVALID_ARGUMENT"},
		{name: "duplicate page", query: "page=1&page=2", code: "COMMON_INVALID_ARGUMENT"},
		{name: "non numeric", query: "page=one", code: "COMMON_INVALID_PAGINATION"},
		{name: "zero", query: "page=0", code: "COMMON_INVALID_PAGINATION"},
		{name: "over cap", query: "page_size=101", code: "COMMON_INVALID_PAGINATION"},
		{name: "overflow", query: "page=9223372036854775807&page_size=100", code: "COMMON_INVALID_PAGINATION"},
		{name: "absolute page cap", query: "page=9223372036854775807&page_size=1", code: "COMMON_INVALID_PAGINATION"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &feedbackCloseRouteRepository{}
			handler := feedbackCloseRouteFixture(t, []string{"feedback.read"}, repository)
			response := serveFeedbackQueryRoute(handler, http.MethodGet, "/customer-portal/api/v1/feedbacks?"+test.query, "", false)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.code) || repository.listCustomer != 0 {
				t.Fatalf("status=%d list_calls=%d body=%s", response.Code, repository.listCustomer, response.Body.String())
			}
		})
	}
}

func TestFeedbackCustomerQueryValidationPreservesPermissionAndCSRFPriority(t *testing.T) {
	repository := &feedbackCloseRouteRepository{}
	readResponse := serveFeedbackQueryRoute(
		feedbackCloseRouteFixture(t, []string{"project.read"}, repository),
		http.MethodGet, "/customer-portal/api/v1/feedbacks?unknown=1", "", false,
	)
	if readResponse.Code != http.StatusForbidden || repository.listCustomer != 0 {
		t.Fatalf("read status=%d repository=%#v", readResponse.Code, repository)
	}

	writeHandler := feedbackCloseRouteFixture(t, []string{"feedback.reply"}, repository)
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/api/v1/feedbacks/feedback-1/close?unknown=1", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("X-CSRF-Token", "1")
	response := httptest.NewRecorder()
	writeHandler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || repository.findCustomer != 0 || repository.findReplay != 0 {
		t.Fatalf("write status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
	}
}

func TestFeedbackCustomerDetailAndWritesRejectAnyQueryBeforeRepository(t *testing.T) {
	tests := []struct {
		name, method, path, body, permission string
		write                                bool
	}{
		{name: "detail", method: http.MethodGet, path: "/customer-portal/api/v1/feedbacks/feedback-1?include=internal", permission: "feedback.read"},
		{name: "create", method: http.MethodPost, path: "/customer-portal/api/v1/feedbacks?customer_id=7", body: `{}`, permission: "feedback.create", write: true},
		{name: "message", method: http.MethodPost, path: "/customer-portal/api/v1/feedbacks/feedback-1/messages?visibility=INTERNAL", body: `{}`, permission: "feedback.reply", write: true},
		{name: "close", method: http.MethodPost, path: "/customer-portal/api/v1/feedbacks/feedback-1/close?force=true", body: `{}`, permission: "feedback.reply", write: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &feedbackCloseRouteRepository{}
			handler := feedbackCloseRouteFixture(t, []string{test.permission}, repository)
			response := serveFeedbackQueryRoute(handler, test.method, test.path, test.body, test.write)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "COMMON_INVALID_ARGUMENT") ||
				repository.createCalls != 0 || repository.findCustomer != 0 || repository.findReplay != 0 || repository.findForUpdate != 0 {
				t.Fatalf("status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
			}
		})
	}
}

func TestFeedbackOperatorListStrictlyBindsQueryAndRejectsMalformedInput(t *testing.T) {
	repository := &feedbackCloseRouteRepository{}
	feedbackService := feedbackCloseRouteService(t, repository)
	authenticator := fakeMachineAuthenticator{principal: sharedauth.Principal{
		TenantID: "tenant-a", UserID: "service-desk",
		Permissions: map[string]struct{}{"portal.feedback.manage": {}},
	}}
	handler := NewRouter(RouterDependencies{
		Config:   Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"},
		Feedback: feedbackService, MachineAuthenticator: authenticator,
	})

	valid := serveFeedbackQueryRoute(handler, http.MethodGet, "/customer-portal/internal/feedbacks?status=PROCESSING&type=SUGGESTION&page=3&page_size=40", "", false)
	if valid.Code != http.StatusOK || repository.listOperator != 1 || repository.lastListQuery.Page != 3 || repository.lastListQuery.PageSize != 40 || repository.lastListQuery.Status != "PROCESSING" || repository.lastListQuery.Type != "SUGGESTION" {
		t.Fatalf("status=%d repository=%#v body=%s", valid.Code, repository, valid.Body.String())
	}

	for _, query := range []string{"unknown=1", "type=COMPLAINT&type=OBJECTION", "page=0", "page_size=101", "page=9223372036854775807&page_size=1"} {
		before := repository.listOperator
		response := serveFeedbackQueryRoute(handler, http.MethodGet, "/customer-portal/internal/feedbacks?"+query, "", false)
		if response.Code != http.StatusBadRequest || repository.listOperator != before {
			t.Fatalf("query=%q status=%d repository=%#v body=%s", query, response.Code, repository, response.Body.String())
		}
	}
}

func TestFeedbackOperatorQueryValidationPreservesScopeAndRejectsActionQuery(t *testing.T) {
	repository := &feedbackCloseRouteRepository{}
	feedbackService := feedbackCloseRouteService(t, repository)
	invalidScope := fakeMachineAuthenticator{principal: sharedauth.Principal{
		TenantID: "tenant-a", UserID: "service-desk",
		Permissions: map[string]struct{}{"report.callback.write": {}},
	}}
	handler := NewRouter(RouterDependencies{
		Config:   Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"},
		Feedback: feedbackService, MachineAuthenticator: invalidScope,
	})
	response := serveFeedbackQueryRoute(handler, http.MethodGet, "/customer-portal/internal/feedbacks?unknown=1", "", false)
	if response.Code != http.StatusForbidden || repository.listOperator != 0 {
		t.Fatalf("scope status=%d repository=%#v", response.Code, repository)
	}

	validScope := fakeMachineAuthenticator{principal: sharedauth.Principal{
		TenantID: "tenant-a", UserID: "service-desk",
		Permissions: map[string]struct{}{"portal.feedback.manage": {}},
	}}
	handler = NewRouter(RouterDependencies{
		Config:   Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"},
		Feedback: feedbackService, MachineAuthenticator: validScope,
	})
	response = serveFeedbackQueryRoute(handler, http.MethodPost, "/customer-portal/internal/feedbacks/feedback-1/accept?force=true", `{}`, false)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "COMMON_INVALID_ARGUMENT") || repository.findOperator != 0 || repository.findForUpdate != 0 {
		t.Fatalf("action status=%d repository=%#v body=%s", response.Code, repository, response.Body.String())
	}
}

func TestFeedbackOperatorListRejectsInvalidBusinessFiltersBeforeRepository(t *testing.T) {
	repository := &feedbackCloseRouteRepository{}
	handler := NewRouter(RouterDependencies{
		Config:   Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"},
		Feedback: feedbackCloseRouteService(t, repository),
		MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{
			TenantID: "tenant-a", UserID: "service-desk",
			Permissions: map[string]struct{}{"portal.feedback.manage": {}},
		}},
	})
	for _, query := range []string{"status=NOT_A_STATUS", "type=NOT_A_TYPE"} {
		response := serveFeedbackQueryRoute(handler, http.MethodGet, "/customer-portal/internal/feedbacks?"+query, "", false)
		if response.Code != http.StatusUnprocessableEntity || repository.listOperator != 0 {
			t.Fatalf("query=%q status=%d repository=%#v body=%s", query, response.Code, repository, response.Body.String())
		}
	}
}
