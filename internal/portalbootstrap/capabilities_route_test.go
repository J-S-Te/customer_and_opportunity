package portalbootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
)

type capabilityReadinessStub struct {
	ready map[string]bool
	err   error
}

func (s capabilityReadinessStub) HasFreshHeartbeat(_ context.Context, workerType string, _ time.Time) (bool, error) {
	return s.ready[workerType], s.err
}

func TestCapabilitiesRequireSessionAndExposeOnlyBoundedRuntimeState(t *testing.T) {
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	router := NewRouter(RouterDependencies{Config: config, Account: reportRiskRouteAccountService(t, nil)})

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/capabilities", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/capabilities", nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var envelope struct {
		Data map[string]struct {
			Available  bool   `json:"available"`
			Mode       string `json:"mode"`
			ReasonCode string `json:"reason_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"report_request_submission": "PORTAL_REPORT_DELIVERY_WORKER_UNAVAILABLE",
		"project_export":            "PORTAL_PROJECT_EXPORT_WORKER_UNAVAILABLE",
		"report_download":           "REPORT_SECURITY_PROVIDERS_NOT_CONFIGURED",
		"filing_material_upload":    "FILING_MATERIAL_PROVIDERS_NOT_CONFIGURED",
		"filing_export":             "FILING_EXPORT_NOT_CONFIGURED",
		"filing_police_submission":  "FILING_POLICE_SUBMISSION_CONTRACT_NOT_CONFIGURED",
	}
	if len(envelope.Data) != len(want) {
		t.Fatalf("capabilities=%#v", envelope.Data)
	}
	for name, reason := range want {
		value, ok := envelope.Data[name]
		if !ok || value.Available || value.ReasonCode != reason || (value.Mode != "UNAVAILABLE" && value.Mode != "LOCAL_ONLY") {
			t.Fatalf("capability %s=%+v", name, value)
		}
	}
	for _, forbidden := range []string{"client", "scope", "secret", "https://", "token", "url"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("response leaked forbidden marker %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestCapabilitiesRouteDoesNotRequireBusinessPermission(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Config:  Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"},
		Account: reportRiskRouteAccountService(t, []string{"project.read"}),
	})
	request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/capabilities", nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCapabilitiesExposeFreshWorkerEvidenceWithoutClaimingRemoteHealth(t *testing.T) {
	router := NewRouter(RouterDependencies{
		Config:  Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"},
		Account: reportRiskRouteAccountService(t, nil),
		WorkerReadiness: capabilityReadinessStub{ready: map[string]bool{
			workerruntime.ReportDeliveryWorker: true,
			workerruntime.ProjectExportWorker:  true,
		}},
		WorkerHeartbeatMaxAge: time.Minute,
	})
	request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/capabilities", nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var envelope struct {
		Data map[string]runtimeCapability `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report_request_submission", "project_export"} {
		if value := envelope.Data[name]; !value.Available || value.Mode != "READY" || value.ReasonCode != "" {
			t.Fatalf("capability %s=%+v", name, value)
		}
	}
	// Report download remains unavailable: worker liveness is not evidence that
	// object storage, scanning, encryption, watermarking or risk providers work.
	if envelope.Data["report_download"].Available {
		t.Fatalf("report download incorrectly inferred from worker heartbeat: %+v", envelope.Data["report_download"])
	}
}
