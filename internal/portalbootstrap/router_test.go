package portalbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

func TestSessionCookieSecurityAttributes(t *testing.T) {
	cookie := sessionCookie(Config{SessionCookieName: "customer_portal_session", PathPrefix: "/customer-portal", SessionCookieSecure: true}, "opaque", time.Now().Add(time.Minute))
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/customer-portal" {
		t.Fatalf("unexpected session cookie: %#v", cookie)
	}
}

func TestPublicProjectDoesNotExposeTenantOrSourceIdentifiers(t *testing.T) {
	value := project.Detail{Snapshot: project.Snapshot{Model: database.Model{ID: 42, TenantID: "tenant-secret", CreatedBy: "actor"}, ProjectID: "project-public"}}
	raw, err := json.Marshal(publicProjectBundle(&value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"tenant-secret", "source-secret", "customer_id", "created_by", "raw_version"} {
		if strings.Contains(text, secret) {
			t.Fatalf("public project response leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"activities":[]`) {
		t.Fatalf("project detail compatibility activities must be bounded and empty: %s", text)
	}
}

func TestProjectPaginationRejectsUnknownDuplicateAndOutOfRangeValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		query string
		code  string
	}{
		{query: "page=one", code: "COMMON_INVALID_PAGINATION"},
		{query: "page=0", code: "COMMON_INVALID_PAGINATION"},
		{query: "page_size=101", code: "COMMON_INVALID_PAGINATION"},
		{query: "page=9223372036854775807&page_size=100", code: "COMMON_INVALID_PAGINATION"},
		{query: "page=1&page=2", code: "COMMON_INVALID_ARGUMENT"},
		{query: "customer_id=99", code: "COMMON_INVALID_ARGUMENT"},
	}
	for _, item := range tests {
		t.Run(item.query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/projects/P-1/activities?"+item.query, nil)
			if _, _, ok := bindProjectPagination(context); ok {
				t.Fatal("invalid project pagination was accepted")
			}
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), item.code) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestProjectPaginationDefaultsAndCapsAreExplicit(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/projects/P-1/activities", nil)
	page, pageSize, ok := bindProjectPagination(context)
	if !ok || page != 1 || pageSize != 20 {
		t.Fatalf("page=%d pageSize=%d ok=%v", page, pageSize, ok)
	}
}

func TestProjectDetailRejectsAllQueryParametersBeforeDataAccess(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/projects/P-1?include=activities", nil)
	getProject(RouterDependencies{})(context)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "COMMON_INVALID_ARGUMENT") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPortalRouterPreservesEncodedSeparatorInsideOpaqueProjectID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	configureOpaquePathRouting(router)
	var projectID string
	router.GET("/projects/:projectID/activities", func(c *gin.Context) {
		projectID = c.Param("projectID")
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/projects/P%2F002/activities", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || projectID != "P/002" {
		t.Fatalf("status=%d projectID=%q", recorder.Code, projectID)
	}
}

func TestPortalRouterSeparatesLivenessAndReadiness(t *testing.T) {
	config := Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}
	router := NewRouter(RouterDependencies{Config: config, DatabaseHealthy: func() bool { return false }})

	live := httptest.NewRecorder()
	router.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/customer-portal/livez", nil))
	if live.Code != http.StatusOK || live.Body.String() != `{"status":"alive"}` {
		t.Fatalf("liveness status=%d body=%s", live.Code, live.Body.String())
	}
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/customer-portal/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || ready.Body.String() != `{"status":"not_ready"}` {
		t.Fatalf("readiness status=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestPublicReportDoesNotExposeSensitivePersistenceFields(t *testing.T) {
	value := report.Request{ActorModel: report.ActorModel{ID: 42, TenantID: "tenant-secret", CreatedBy: "actor", Version: 3}, RequestNo: "RR-1", ReceiveEmailCipher: []byte("cipher-secret"), IdempotencyKey: "idem-secret", RequestHash: "hash-secret"}
	raw, err := json.Marshal(publicReport(&value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"cipher-secret", "idem-secret", "hash-secret", "tenant-secret", "created_by", "receive_email_cipher", "idempotency_key", "request_hash"} {
		if strings.Contains(text, secret) {
			t.Fatalf("public response leaked %q: %s", secret, text)
		}
	}
}

func TestPublicReportDetailExposesMinimalTimelineOnly(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	value := report.Detail{
		Request: &report.Request{ActorModel: report.ActorModel{ID: 42, TenantID: "tenant-secret", Version: 3}, RequestNo: "RR-1"},
		Events:  []report.StatusEvent{{ID: 7, TenantID: "tenant-secret", CustomerID: 99, RequestID: 42, EventType: "APPROVAL_STARTED", Sequence: 4, FromStatus: report.StatusSubmitted, ToStatus: report.StatusApproving, ActorType: "SYSTEM", ActorID: "worker-secret", SourceKeyHash: "source-secret", PayloadHash: "payload-secret", RequestTrace: "trace-secret", OccurredAt: now}},
	}
	raw, err := json.Marshal(publicReportDetail(&value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{"APPROVAL_STARTED", "SUBMITTED", "APPROVING", now.Format(time.RFC3339)} {
		if !strings.Contains(text, expected) {
			t.Fatalf("public detail missing %q: %s", expected, text)
		}
	}
	for _, secret := range []string{"tenant-secret", "worker-secret", "source-secret", "payload-secret", "trace-secret", "customer_id", "actor_id", "source_key_hash", "payload_hash", "request_trace"} {
		if strings.Contains(text, secret) {
			t.Fatalf("public detail leaked %q: %s", secret, text)
		}
	}
}

func TestReportGrantResponseDoesNotExposeStoredTokenHash(t *testing.T) {
	value := report.GrantResult{GrantID: "grant-public", Status: report.GrantActive, ExpiresAt: time.Now().UTC(), DownloadToken: "plaintext-once"}
	raw, err := json.Marshal(gin.H{"grant_id": value.GrantID, "status": value.Status, "expires_at": value.ExpiresAt, "download_token": value.DownloadToken})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "plaintext-once") || strings.Contains(text, "token_hash") || strings.Contains(text, "issue_key_hash") || strings.Contains(text, "tenant_id") || strings.Contains(text, "customer_id") || strings.Contains(text, "account_id") {
		t.Fatalf("unsafe grant DTO: %s", text)
	}
}

func TestReportDownloadContractKeepsTokenOutOfURLAndUsesSafeDisposition(t *testing.T) {
	if got := contentDisposition("交付报告.pdf"); strings.ContainsAny(got, "\r\n") || !strings.HasPrefix(got, "attachment; filename=report.pdf; filename*=UTF-8''") {
		t.Fatalf("unsafe content disposition: %q", got)
	}
	router := NewRouter(RouterDependencies{Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}})
	for _, route := range router.Routes() {
		if strings.Contains(route.Path, ":token") || strings.Contains(route.Path, "{token}") {
			t.Fatalf("download credential must not be a URL parameter: %s", route.Path)
		}
	}
}

func TestReportNotificationRoutesUseReportReadPermission(t *testing.T) {
	router := NewRouter(RouterDependencies{Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}})
	paths := map[string]bool{
		"GET /customer-portal/api/v1/report-notifications":              false,
		"GET /customer-portal/api/v1/report-notifications/unread-count": false,
		"POST /customer-portal/api/v1/report-notifications/:id/read":    false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := paths[key]; exists {
			paths[key] = true
		}
	}
	for route, found := range paths {
		if !found {
			t.Fatalf("missing Portal report notification route %s", route)
		}
	}
}

func TestReportDownloadCompletionErrorHookDoesNotRequireSensitiveContext(t *testing.T) {
	want := errors.New("audit unavailable")
	called := false
	deps := RouterDependencies{ReportDownloadError: func(_ context.Context, err error) {
		called = errors.Is(err, want)
	}}
	deps.ReportDownloadError(context.Background(), want)
	if !called {
		t.Fatal("download completion audit error hook was not invoked")
	}
}

func TestOriginAndCSRFRejectsCrossOriginAndMissingHeader(t *testing.T) {
	tests := []struct {
		name, origin, csrf string
		status             int
	}{
		{"cross origin", "https://evil.example", "1", http.StatusForbidden},
		{"missing CSRF header", "https://portal.example", "", http.StatusForbidden},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/customer-portal/api/v1/reports", nil)
			request.Header.Set("Origin", item.origin)
			request.Header.Set("X-CSRF-Token", item.csrf)
			probe := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(probe)
			context.Request = request
			originAndCSRF(Config{PublicOrigin: "https://portal.example"})(context)
			if probe.Code != item.status {
				t.Fatalf("status=%d want=%d", probe.Code, item.status)
			}
		})
	}
}

func TestRequireAnyPermissionAllowsEitherEvaluationCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		permissions []string
		allowed     bool
	}{
		{name: "read", permissions: []string{"evaluation.read"}, allowed: true},
		{name: "create", permissions: []string{"evaluation.create"}, allowed: true},
		{name: "unrelated", permissions: []string{"project.read"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			called := false
			router.GET("/", func(c *gin.Context) {
				c.Set(sessionContextKey, &account.Session{Permissions: test.permissions})
				c.Next()
			}, requireAnyPermission("evaluation.read", "evaluation.create"), func(*gin.Context) { called = true })
			probe := httptest.NewRecorder()
			router.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/", nil))
			if called != test.allowed {
				t.Fatalf("called=%v, want %v", called, test.allowed)
			}
			if !test.allowed && probe.Code != http.StatusForbidden {
				t.Fatalf("status=%d want=%d", probe.Code, http.StatusForbidden)
			}
		})
	}
}

func TestPortalPermissionMiddlewareSeparatesFeedbackAndSecurityCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		required    string
		permissions []string
		allowed     bool
	}{
		{name: "feedback list accepts read", required: "feedback.read", permissions: []string{"feedback.read"}, allowed: true},
		{name: "feedback list rejects create", required: "feedback.read", permissions: []string{"feedback.create"}},
		{name: "feedback create accepts create", required: "feedback.create", permissions: []string{"feedback.create"}, allowed: true},
		{name: "feedback create rejects reply", required: "feedback.create", permissions: []string{"feedback.reply"}},
		{name: "feedback reply accepts reply", required: "feedback.reply", permissions: []string{"feedback.reply"}, allowed: true},
		{name: "feedback reply rejects create", required: "feedback.reply", permissions: []string{"feedback.create"}},
		{name: "security accepts manage", required: "account.security.manage", permissions: []string{"account.security.manage"}, allowed: true},
		{name: "security rejects feedback", required: "account.security.manage", permissions: []string{"feedback.read", "feedback.reply"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			called := false
			router.GET("/", func(c *gin.Context) {
				c.Set(sessionContextKey, &account.Session{Permissions: test.permissions})
				c.Next()
			}, requirePermission(test.required), func(*gin.Context) { called = true })
			probe := httptest.NewRecorder()
			router.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/", nil))
			if called != test.allowed {
				t.Fatalf("called=%v, want %v", called, test.allowed)
			}
			if !test.allowed && probe.Code != http.StatusForbidden {
				t.Fatalf("status=%d want=%d", probe.Code, http.StatusForbidden)
			}
		})
	}
}

func TestMachineAuthRejectsInvalidToken(t *testing.T) {
	router := NewRouter(RouterDependencies{Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}, MachineAuthenticator: fakeMachineAuthenticator{err: errors.New("invalid")}})
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/accounts/provision", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401", response.Code)
	}
}

func TestInternalRoutesEnforceSeparateMinimumScopes(t *testing.T) {
	tests := []struct {
		name, path, scope string
		status            int
	}{
		{name: "provision accepts mapping scope", path: "/customer-portal/internal/accounts/provision", scope: "portal.identity_mapping.provision", status: http.StatusUnprocessableEntity},
		{name: "provision rejects callback scope", path: "/customer-portal/internal/accounts/provision", scope: "report.callback.write", status: http.StatusForbidden},
		{name: "callback accepts callback scope", path: "/customer-portal/internal/report-callbacks", scope: "report.callback.write", status: http.StatusUnprocessableEntity},
		{name: "callback rejects mapping scope", path: "/customer-portal/internal/report-callbacks", scope: "portal.identity_mapping.provision", status: http.StatusForbidden},
		{name: "evaluation notice accepts evaluation scope", path: "/customer-portal/internal/evaluations/evaluation-public/low-score-notice/read", scope: "portal.evaluation.read", status: http.StatusUnprocessableEntity},
		{name: "evaluation notice rejects feedback scope", path: "/customer-portal/internal/evaluations/evaluation-public/low-score-notice/read", scope: "portal.feedback.manage", status: http.StatusForbidden},
		{name: "filing unlock accepts filing scope", path: "/customer-portal/internal/filings/filing-public/unlock", scope: "portal.filing.unlock", status: http.StatusUnprocessableEntity},
		{name: "filing unlock rejects feedback scope", path: "/customer-portal/internal/filings/filing-public/unlock", scope: "portal.feedback.manage", status: http.StatusForbidden},
		{name: "filing scan accepts scan scope", path: "/customer-portal/internal/filing-material-scan-callbacks", scope: "portal.filing_material.scan.write", status: http.StatusServiceUnavailable},
		{name: "filing scan rejects filing unlock scope", path: "/customer-portal/internal/filing-material-scan-callbacks", scope: "portal.filing.unlock", status: http.StatusForbidden},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			permissions := map[string]struct{}{item.scope: {}}
			config := Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", CRMProvisionClientSubject: "crm-portal-provision", CRMDisableClientSubject: "crm-portal-disable"}
			principal := sharedauth.Principal{TenantID: "tenant-a", Permissions: permissions}
			if item.path == "/customer-portal/internal/accounts/provision" {
				principal.UserID = "machine:crm-portal-provision"
			}
			router := NewRouter(RouterDependencies{Config: config, MachineAuthenticator: fakeMachineAuthenticator{principal: principal}})
			request := httptest.NewRequest(http.MethodPost, item.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != item.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, item.status, response.Body.String())
			}
		})
	}
}

func TestInternalRouteRejectsOverScopedMachineToken(t *testing.T) {
	permissions := map[string]struct{}{"portal.identity_mapping.provision": {}, "report.callback.write": {}}
	router := NewRouter(RouterDependencies{Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}, MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{TenantID: "tenant-a", Permissions: permissions}}})
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/accounts/provision", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d", response.Code, http.StatusForbidden)
	}
}

func TestAccountProvisionUsesOnlyAuthenticatedMachineTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const validBody = `{"tenant_id":"tenant-a","account_no":"EXT-1","platform_user_id":"subject-a","display_name":"Customer","customer_id":7,"contact_id":9}`
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCalls  int
	}{
		{name: "matching tenant", body: validBody, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "omitted tenant", body: `{"account_no":"EXT-1","platform_user_id":"subject-a","display_name":"Customer","customer_id":7,"contact_id":9}`, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "cross tenant", body: strings.Replace(validBody, "tenant-a", "tenant-b", 1), wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			deps := RouterDependencies{
				Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", CRMProvisionClientSubject: "crm-portal-provision"},
				MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{
					UserID: "machine:crm-portal-provision", TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.identity_mapping.provision": {}},
				}},
				ProvisionAccount: func(_ context.Context, command account.ProvisionCommand) (account.ProvisionResult, error) {
					calls++
					if command.TenantID != "tenant-a" || command.CustomerID != 7 || command.PlatformUserID != "subject-a" {
						t.Fatalf("provision command = %#v", command)
					}
					return account.ProvisionResult{PortalAccountID: "PA7", AccountNo: command.AccountNo}, nil
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/accounts/provision", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			NewRouter(deps).ServeHTTP(response, request)
			if response.Code != test.wantStatus || calls != test.wantCalls {
				t.Fatalf("status=%d calls=%d want status=%d calls=%d body=%s", response.Code, calls, test.wantStatus, test.wantCalls, response.Body.String())
			}
		})
	}
}

func TestAccountReconciliationSnapshotUsesAuthenticatedTenantAndExactMachineClient(t *testing.T) {
	for _, item := range []struct {
		name, subject string
		status        int
		calls         int
	}{
		{name: "configured client", subject: "crm-portal-provision", status: http.StatusOK, calls: 1},
		{name: "different client", subject: "other-client", status: http.StatusForbidden},
	} {
		t.Run(item.name, func(t *testing.T) {
			calls := 0
			deps := RouterDependencies{
				Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", CRMProvisionClientSubject: "crm-portal-provision"},
				MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{
					UserID: "machine:" + item.subject, TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.identity_mapping.provision": {}},
				}},
				ReconcileAccounts: func(_ context.Context, tenantID string, subjects []string) ([]account.ReconciliationSnapshot, error) {
					calls++
					if tenantID != "tenant-a" || len(subjects) != 1 || subjects[0] != "subject-a" {
						t.Fatalf("tenant=%q subjects=%#v", tenantID, subjects)
					}
					return []account.ReconciliationSnapshot{{PlatformUserID: "subject-a", Found: false}}, nil
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/accounts/reconciliation-snapshot", strings.NewReader(`{"items":[{"platform_user_id":"subject-a"}]}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			NewRouter(deps).ServeHTTP(response, request)
			if response.Code != item.status || calls != item.calls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestAccountRoutesRequireConfiguredExactMachineClientSubject(t *testing.T) {
	for _, item := range []struct {
		name, path, scope, expectedSubject, actualSubject string
		wantStatus                                        int
	}{
		{name: "provision accepts configured client", path: "/customer-portal/internal/accounts/provision", scope: "portal.identity_mapping.provision", expectedSubject: "crm-portal-provision", actualSubject: "crm-portal-provision", wantStatus: http.StatusUnprocessableEntity},
		{name: "provision rejects another client", path: "/customer-portal/internal/accounts/provision", scope: "portal.identity_mapping.provision", expectedSubject: "crm-portal-provision", actualSubject: "other-client", wantStatus: http.StatusForbidden},
		{name: "disable accepts configured client", path: "/customer-portal/internal/accounts/disable", scope: "portal.identity_mapping.disable", expectedSubject: "crm-portal-disable", actualSubject: "crm-portal-disable", wantStatus: http.StatusUnprocessableEntity},
		{name: "disable rejects provision client", path: "/customer-portal/internal/accounts/disable", scope: "portal.identity_mapping.disable", expectedSubject: "crm-portal-disable", actualSubject: "crm-portal-provision", wantStatus: http.StatusForbidden},
		{name: "missing expected client fails closed", path: "/customer-portal/internal/accounts/disable", scope: "portal.identity_mapping.disable", actualSubject: "crm-portal-disable", wantStatus: http.StatusForbidden},
	} {
		t.Run(item.name, func(t *testing.T) {
			config := Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}
			if item.scope == "portal.identity_mapping.provision" {
				config.CRMProvisionClientSubject = item.expectedSubject
			} else {
				config.CRMDisableClientSubject = item.expectedSubject
			}
			principal := sharedauth.Principal{UserID: "machine:" + item.actualSubject, TenantID: "tenant-a", Permissions: map[string]struct{}{item.scope: {}}}
			request := httptest.NewRequest(http.MethodPost, item.path, nil)
			response := httptest.NewRecorder()
			NewRouter(RouterDependencies{Config: config, MachineAuthenticator: fakeMachineAuthenticator{principal: principal}}).ServeHTTP(response, request)
			if response.Code != item.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, item.wantStatus, response.Body.String())
			}
		})
	}
}

func TestAccountDisableBindsAuthenticatedTenantActorAndIdempotency(t *testing.T) {
	const body = `{"tenant_id":"tenant-a","customer_id":7,"platform_user_id":"subject-a","reason":"customer administrator revoked access"}`
	for _, item := range []struct {
		name, body string
		wantStatus int
		wantCalls  int
	}{
		{name: "valid request", body: body, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "cross tenant body", body: strings.Replace(body, "tenant-a", "tenant-b", 1), wantStatus: http.StatusForbidden},
	} {
		t.Run(item.name, func(t *testing.T) {
			calls := 0
			deps := RouterDependencies{
				Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", CRMDisableClientSubject: "crm-portal-disable"},
				MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{
					UserID: "machine:crm-portal-disable", TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.identity_mapping.disable": {}},
				}},
				DisableAccount: func(_ context.Context, command account.DisableCommand) (account.DisableResult, error) {
					calls++
					if command.TenantID != "tenant-a" || command.ActorID != "machine:crm-portal-disable" || command.CustomerID != 7 || command.PlatformUserID != "subject-a" || command.IdempotencyKey != "disable-business-key" {
						t.Fatalf("disable command=%#v", command)
					}
					return account.DisableResult{CustomerID: 7, PlatformUserID: "subject-a", Status: account.IdentityDisabled, Version: 2}, nil
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/accounts/disable", strings.NewReader(item.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "disable-business-key")
			response := httptest.NewRecorder()
			NewRouter(deps).ServeHTTP(response, request)
			if response.Code != item.wantStatus || calls != item.wantCalls {
				t.Fatalf("status=%d calls=%d want status=%d calls=%d body=%s", response.Code, calls, item.wantStatus, item.wantCalls, response.Body.String())
			}
		})
	}
}

func TestAccountDisableRequiresBusinessIdempotencyKey(t *testing.T) {
	called := false
	deps := RouterDependencies{
		Config: Config{PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", CRMDisableClientSubject: "crm-portal-disable"},
		MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{
			UserID: "machine:crm-portal-disable", TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.identity_mapping.disable": {}},
		}},
		DisableAccount: func(context.Context, account.DisableCommand) (account.DisableResult, error) {
			called = true
			return account.DisableResult{}, account.ErrInvalidClaims
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/accounts/disable", strings.NewReader(`{"customer_id":7,"platform_user_id":"subject-a","reason":"revoked"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewRouter(deps).ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || called {
		t.Fatalf("status=%d called=%v body=%s", response.Code, called, response.Body.String())
	}
}

func TestReportCallbackRejectsBodyTenantDifferentFromMachineTenant(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/report-callbacks", strings.NewReader(`{"tenant_id":"tenant-b","request_id":7,"customer_id":9,"project_id":"project-1","version":1,"status":"APPROVED_PROCESSING","downstream_request_id":"PS-7","approval_result":"APPROVED"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "callback-1")
	request = request.WithContext(sharedauth.WithPrincipal(request.Context(), sharedauth.Principal{TenantID: "tenant-a"}))
	probe := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(probe)
	context.Request = request
	reportCallback(RouterDependencies{})(context)
	if probe.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", probe.Code, http.StatusForbidden, probe.Body.String())
	}
}

func TestDecodeRejectsInvalidJSONObjectsWithoutReflectingBodyDetails(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"name":"valid","password_secret_DO_NOT_LEAK":"value"}`},
		{name: "trailing object", body: `{"name":"valid"}{"password_secret_DO_NOT_LEAK":"value"}`},
		{name: "empty", body: ``},
		{name: "null", body: `null`},
		{name: "array", body: `[{"name":"valid"}]`},
		{name: "scalar", body: `"password_secret_DO_NOT_LEAK"`},
		{name: "over limit", body: `{"name":"` + strings.Repeat("x", (1<<20)+1) + `password_secret_DO_NOT_LEAK"}`},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/test", strings.NewReader(item.body))
			probe := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(probe)
			context.Request = request
			var body struct {
				Name string `json:"name" binding:"required"`
			}

			if decode(context, &body) {
				t.Fatal("decode unexpectedly accepted invalid request body")
			}
			if probe.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d want=%d body=%s", probe.Code, http.StatusUnprocessableEntity, probe.Body.String())
			}
			var envelope struct {
				Code    string          `json:"code"`
				Message string          `json:"message"`
				Details json.RawMessage `json:"details"`
			}
			if err := json.Unmarshal(probe.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("invalid error envelope: %v body=%s", err, probe.Body.String())
			}
			if envelope.Code != "COMMON_VALIDATION_ERROR" || envelope.Message != "request body is invalid" {
				t.Fatalf("unexpected error envelope: %s", probe.Body.String())
			}
			if len(envelope.Details) != 0 {
				t.Fatalf("parser details must be omitted: %s", probe.Body.String())
			}
			if strings.Contains(probe.Body.String(), "password_secret_DO_NOT_LEAK") || strings.Contains(probe.Body.String(), "unknown field") {
				t.Fatalf("error response reflected parser or request-body details: %s", probe.Body.String())
			}
		})
	}
}

func TestDecodeAcceptsOneValidJSONObjectAndRunsBindingValidation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/test", strings.NewReader(`{"name":"valid"}`))
	probe := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(probe)
	context.Request = request
	var body struct {
		Name string `json:"name" binding:"required"`
	}
	if !decode(context, &body) {
		t.Fatalf("decode rejected valid body: status=%d body=%s", probe.Code, probe.Body.String())
	}
	if body.Name != "valid" {
		t.Fatalf("decoded name=%q", body.Name)
	}

	missingRequest := httptest.NewRequest(http.MethodPost, "/customer-portal/internal/test", strings.NewReader(`{}`))
	missingProbe := httptest.NewRecorder()
	missingContext, _ := gin.CreateTestContext(missingProbe)
	missingContext.Request = missingRequest
	var missingBody struct {
		Name string `json:"name" binding:"required"`
	}
	if decode(missingContext, &missingBody) {
		t.Fatal("decode must preserve Gin binding validation")
	}
	if missingProbe.Code != http.StatusUnprocessableEntity {
		t.Fatalf("binding status=%d want=%d", missingProbe.Code, http.StatusUnprocessableEntity)
	}
}

type fakeMachineAuthenticator struct {
	principal sharedauth.Principal
	err       error
}

func (f fakeMachineAuthenticator) Authenticate(context.Context, *http.Request) (sharedauth.Principal, error) {
	return f.principal, f.err
}

func TestMetadataHashUsesSecretHMACKey(t *testing.T) {
	left := hashText([]byte("01234567890123456789012345678901"), "192.0.2.1")
	right := hashText([]byte("abcdefghijklmnopqrstuvwxyz123456"), "192.0.2.1")
	if left == right || left == "" || right == "" {
		t.Fatalf("metadata hash must be keyed: left=%q right=%q", left, right)
	}
	if left != hashText([]byte("01234567890123456789012345678901"), "192.0.2.1") {
		t.Fatal("same HMAC key and input must be stable")
	}
}

func TestSecurityMetadataUsesDirectPeerAndIgnoresForwardedHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://portal.example/customer-portal/auth/callback", nil)
	request.RemoteAddr = "192.0.2.45:4321"
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	if got := directRemoteIP(request.RemoteAddr); got != "192.0.2.45" {
		t.Fatalf("directRemoteIP()=%q", got)
	}
	if got := maskIPAddress(directRemoteIP(request.RemoteAddr)); got != "192.0.0.0" {
		t.Fatalf("maskIPAddress()=%q", got)
	}
}

func TestConfiguredSecurityCenterCannotUseRequestOverride(t *testing.T) {
	config := Config{AccountSecurityCenterURL: "https://identity.example/account/security", PublicOrigin: "https://portal.example", PathPrefix: "/customer-portal"}
	got := configuredSecurityCenterURL(config)
	if got != "https://identity.example/account/security?return_to=https%3A%2F%2Fportal.example%2Fcustomer-portal%2Fsecurity" {
		t.Fatalf("configuredSecurityCenterURL()=%q", got)
	}
}

func TestRevokingCurrentSessionClearsPortalCookie(t *testing.T) {
	// Cookie construction is shared by logout and current-session revocation;
	// MaxAge=-1 and the same path/name force immediate browser removal.
	config := Config{SessionCookieName: "customer_portal_session", PathPrefix: "/customer-portal", SessionCookieSecure: true}
	cookie := sessionCookie(config, "", time.Unix(1, 0))
	cookie.MaxAge = -1
	if cookie.Name != config.SessionCookieName || cookie.Path != config.PathPrefix || cookie.MaxAge != -1 || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("expired session cookie=%#v", cookie)
	}
}

func TestDeviceSummaryUsesDeterministicPriority(t *testing.T) {
	tests := map[string]string{
		"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/125 Safari/537.36":       "Android · Chrome",
		"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/125 Safari/537.36 Edg/125": "Windows · Edge",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) AppleWebKit/605.1 Safari/604.1":           "iPhone · Safari",
	}
	for userAgent, want := range tests {
		if got := deviceSummary(userAgent); got != want {
			t.Errorf("deviceSummary()=%q want=%q", got, want)
		}
	}
}
