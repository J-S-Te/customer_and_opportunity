package portalbootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

// reportOutboxRouteRepo 以租户为范围模拟投递死信存取，用于验证机器接口不能越过验签主体租户。
type reportOutboxRouteRepo struct {
	reportNotificationRouteRepository
	rows          map[string][]report.OutboxFailureView
	listTenants   []string
	listPages     [][2]int
	retriedTenant string
	retriedEvent  string
	retryResult   *report.OutboxRetryResult
}

func (r *reportOutboxRouteRepo) ListOutboxDeadLetters(_ context.Context, tenantID string, page, pageSize int) (pagination.Page[report.OutboxFailureView], error) {
	r.listTenants = append(r.listTenants, tenantID)
	r.listPages = append(r.listPages, [2]int{page, pageSize})
	items := append([]report.OutboxFailureView{}, r.rows[tenantID]...)
	return pagination.Page[report.OutboxFailureView]{Items: items, Page: page, PageSize: pageSize, Total: int64(len(items))}, nil
}

func (r *reportOutboxRouteRepo) RetryOutboxDeadLetter(_ context.Context, tenantID, eventID string) (report.OutboxRetryResult, error) {
	r.retriedTenant, r.retriedEvent = tenantID, eventID
	if r.retryResult != nil {
		return *r.retryResult, nil
	}
	for _, row := range r.rows[tenantID] {
		if row.EventID == eventID {
			return report.OutboxRetryApplied, nil
		}
	}
	return report.OutboxRetryNotFound, nil
}

func reportOutboxRouteFixture(repository *reportOutboxRouteRepo, authenticator machineAuthenticator) http.Handler {
	service := report.NewService(repository, nil, nil, nil, reportNotificationRouteClock{now: time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)}, nil)
	return NewRouter(RouterDependencies{
		Config:               Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"},
		Reports:              service,
		MachineAuthenticator: authenticator,
	})
}

func reportOutboxRoutePrincipal(tenantID string, permissions ...string) sharedauth.Principal {
	scopes := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		scopes[permission] = struct{}{}
	}
	return sharedauth.Principal{UserID: "machine:delivery-ops", TenantID: tenantID, Permissions: scopes}
}

func serveReportOutboxRoute(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestInternalReportOutboxRoutesRequireMachineAuthentication(t *testing.T) {
	repository := &reportOutboxRouteRepo{}
	handler := reportOutboxRouteFixture(repository, nil)
	for _, target := range []string{"/customer-portal/internal/report-outbox/failed?page=1&page_size=20", "/customer-portal/internal/report-outbox/failed/evt-1/retry"} {
		method := http.MethodGet
		if target[len(target)-5:] == "retry" {
			method = http.MethodPost
		}
		response := serveReportOutboxRoute(handler, method, target, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	if len(repository.listTenants) != 0 || repository.retriedEvent != "" {
		t.Fatalf("unauthenticated request reached repository: %+v", repository)
	}
}

func TestInternalReportOutboxRoutesRequireExactDeliveryManageScope(t *testing.T) {
	repository := &reportOutboxRouteRepo{rows: map[string][]report.OutboxFailureView{"tenant-a": {{EventID: "evt-1", RequestID: 7, RetryCount: 7, LastErrorSummary: "downstream unavailable"}}}}
	for _, test := range []struct {
		principal sharedauth.Principal
		status    int
	}{
		{reportOutboxRoutePrincipal("tenant-a", "portal.report.risk.manage"), http.StatusForbidden},
		{reportOutboxRoutePrincipal("tenant-a", "portal.report.delivery.manage", "report.callback.write"), http.StatusForbidden},
		{reportOutboxRoutePrincipal("tenant-a", "portal.report.delivery.manage"), http.StatusOK},
	} {
		handler := reportOutboxRouteFixture(repository, fakeMachineAuthenticator{principal: test.principal})
		response := serveReportOutboxRoute(handler, http.MethodGet, "/customer-portal/internal/report-outbox/failed?page=1&page_size=20", "")
		if response.Code != test.status {
			t.Fatalf("scopes=%v status=%d body=%s", test.principal.Permissions, response.Code, response.Body.String())
		}
	}
	if len(repository.listTenants) != 1 || repository.listTenants[0] != "tenant-a" {
		t.Fatalf("allowed request scopes=%v", repository.listTenants)
	}
}

func TestInternalReportOutboxListUsesVerifiedPrincipalTenant(t *testing.T) {
	repository := &reportOutboxRouteRepo{rows: map[string][]report.OutboxFailureView{
		"tenant-a": {{EventID: "evt-a", RequestID: 7, RetryCount: 7, LastErrorSummary: "downstream unavailable"}},
		"tenant-b": {{EventID: "evt-b", RequestID: 9, RetryCount: 7, LastErrorSummary: "timeout"}},
	}}
	handler := reportOutboxRouteFixture(repository, fakeMachineAuthenticator{principal: reportOutboxRoutePrincipal("tenant-b", "portal.report.delivery.manage")})
	response := serveReportOutboxRoute(handler, http.MethodGet, "/customer-portal/internal/report-outbox/failed?page=2&page_size=5", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.listTenants) != 1 || repository.listTenants[0] != "tenant-b" || repository.listPages[0] != [2]int{2, 5} {
		t.Fatalf("repository scope=%v pages=%v", repository.listTenants, repository.listPages)
	}
	var envelope struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int64            `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(envelope.Data.Items) != 1 || envelope.Data.Items[0]["event_id"] != "evt-b" {
		t.Fatalf("items=%+v", envelope.Data.Items)
	}
	// 脱敏：死信列表不下行载荷、事件类型或租户身份。
	for _, key := range []string{"payload", "event_type", "tenant_id", "locked_by", "id"} {
		if _, ok := envelope.Data.Items[0][key]; ok {
			t.Fatalf("list projection leaked %q: %+v", key, envelope.Data.Items[0])
		}
	}
}

func TestInternalReportOutboxListRejectsTenantOverrideQuery(t *testing.T) {
	repository := &reportOutboxRouteRepo{}
	handler := reportOutboxRouteFixture(repository, fakeMachineAuthenticator{principal: reportOutboxRoutePrincipal("tenant-a", "portal.report.delivery.manage")})
	for _, target := range []string{
		"/customer-portal/internal/report-outbox/failed?tenant_id=tenant-b",
		"/customer-portal/internal/report-outbox/failed?page=1&page_size=20&status=DEAD_LETTER",
	} {
		response := serveReportOutboxRoute(handler, http.MethodGet, target, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	if len(repository.listTenants) != 0 {
		t.Fatalf("invalid query reached repository: %v", repository.listTenants)
	}
}

func TestInternalReportOutboxRetryIsTenantScopedAndIdempotent(t *testing.T) {
	repository := &reportOutboxRouteRepo{rows: map[string][]report.OutboxFailureView{
		"tenant-a": {{EventID: "evt-a", RequestID: 7, RetryCount: 7}},
	}}
	handler := reportOutboxRouteFixture(repository, fakeMachineAuthenticator{principal: reportOutboxRoutePrincipal("tenant-a", "portal.report.delivery.manage")})
	response := serveReportOutboxRoute(handler, http.MethodPost, "/customer-portal/internal/report-outbox/failed/evt-a/retry", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.retriedTenant != "tenant-a" || repository.retriedEvent != "evt-a" {
		t.Fatalf("retry scope=(%q,%q)", repository.retriedTenant, repository.retriedEvent)
	}
	if !jsonContainsStatus(response.Body.Bytes(), "RETRY_WAIT") {
		t.Fatalf("body=%s", response.Body.String())
	}

	// 跨租户：其他租户即使知道事件身份也拿不到本租户死信。
	repository.retriedTenant, repository.retriedEvent = "", ""
	handler = reportOutboxRouteFixture(repository, fakeMachineAuthenticator{principal: reportOutboxRoutePrincipal("tenant-b", "portal.report.delivery.manage")})
	response = serveReportOutboxRoute(handler, http.MethodPost, "/customer-portal/internal/report-outbox/failed/evt-a/retry?tenant_id=tenant-a", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("tenant override status=%d body=%s", response.Code, response.Body.String())
	}
	response = serveReportOutboxRoute(handler, http.MethodPost, "/customer-portal/internal/report-outbox/failed/evt-a/retry", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross tenant status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.retriedTenant != "tenant-b" || repository.retriedEvent != "evt-a" {
		t.Fatalf("cross tenant retry scope=(%q,%q)", repository.retriedTenant, repository.retriedEvent)
	}

	// 非死信态（已被其他运维会话重试）返回冲突而不是伪造成功。
	notRetryable := report.OutboxRetryNotRetryable
	repository.retryResult = &notRetryable
	handler = reportOutboxRouteFixture(repository, fakeMachineAuthenticator{principal: reportOutboxRoutePrincipal("tenant-a", "portal.report.delivery.manage")})
	response = serveReportOutboxRoute(handler, http.MethodPost, "/customer-portal/internal/report-outbox/failed/evt-a/retry", "")
	if response.Code != http.StatusConflict {
		t.Fatalf("not retryable status=%d body=%s", response.Code, response.Body.String())
	}
}

func jsonContainsStatus(body []byte, status string) bool {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	value, _ := envelope.Data["status"].(string)
	return value == status
}
