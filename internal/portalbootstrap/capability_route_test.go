package portalbootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/capability"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type capabilityStub struct {
	options     capability.Options
	getCalls    int
	upsertCalls int
}

func (s *capabilityStub) Get(context.Context, string, uint64) (capability.Options, error) {
	s.getCalls++
	return s.options, nil
}

func (s *capabilityStub) Upsert(_ context.Context, _ string, _ uint64, options capability.Options) (capability.Options, error) {
	s.upsertCalls++
	s.options = options
	return options, nil
}

func portalCapabilitiesRouter(t *testing.T, store capability.Store) *gin.Engine {
	t.Helper()
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	return NewRouter(RouterDependencies{
		Config: config, Account: reportRiskRouteAccountService(t, []string{"project.read", "report.read", "account.security.manage"}),
		CustomerCapabilities: store,
	})
}

func TestPortalAuthIntersectsCustomerCapabilities(t *testing.T) {
	options := capability.DefaultOptions()
	options.ReportEnabled = false
	store := &capabilityStub{options: options}
	router := portalCapabilitiesRouter(t, store)

	request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	permissions := strings.Join(envelope.Data.Permissions, ",")
	if !strings.Contains(permissions, "project.read") || !strings.Contains(permissions, "account.security.manage") {
		t.Fatalf("expected non-gated permissions, got %q", permissions)
	}
	if strings.Contains(permissions, "report.read") {
		t.Fatalf("report.read must be filtered out when report_enabled=false, got %q", permissions)
	}
}

func TestCapabilitiesRouteExposesCustomerServiceOptions(t *testing.T) {
	options := capability.DefaultOptions()
	options.FeedbackEnabled = false
	router := portalCapabilitiesRouter(t, &capabilityStub{options: options})

	request := httptest.NewRequest(http.MethodGet, "/customer-portal/api/v1/capabilities", nil)
	request.AddCookie(&http.Cookie{Name: "portal_session", Value: "opaque-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Customer customerCapabilities `json:"customer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Customer.FeedbackEnabled || !envelope.Data.Customer.ProjectEnabled || !envelope.Data.Customer.ReportEnabled {
		t.Fatalf("customer capabilities=%+v", envelope.Data.Customer)
	}
}

func TestInternalCustomerCapabilitiesGetAndUpdate(t *testing.T) {
	store := &capabilityStub{options: capability.DefaultOptions()}
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}
	readRouter := NewRouter(RouterDependencies{
		Config: config, CustomerCapabilities: store,
		MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.customer_capabilities.read": {}}}},
	})
	readRequest := httptest.NewRequest(http.MethodGet, "/customer-portal/internal/customers/7/capabilities", nil)
	readResponse := httptest.NewRecorder()
	readRouter.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	if store.getCalls != 1 {
		t.Fatalf("getCalls=%d", store.getCalls)
	}

	writeRouter := NewRouter(RouterDependencies{
		Config: config, CustomerCapabilities: store,
		MachineAuthenticator: fakeMachineAuthenticator{principal: sharedauth.Principal{TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.customer_capabilities.manage": {}}}},
	})
	writeRequest := httptest.NewRequest(http.MethodPut, "/customer-portal/internal/customers/7/capabilities", strings.NewReader(`{"capabilities":{"report_enabled":false}}`))
	writeRequest.Header.Set("Content-Type", "application/json")
	writeResponse := httptest.NewRecorder()
	writeRouter.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("write status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}
	if store.upsertCalls != 1 || store.options.ReportEnabled {
		t.Fatalf("upsertCalls=%d options=%+v", store.upsertCalls, store.options)
	}

	forbidden := httptest.NewRecorder()
	writeRouter.ServeHTTP(forbidden, httptest.NewRequest(http.MethodPut, "/customer-portal/internal/customers/7/capabilities", strings.NewReader(`{"capabilities":{"unknown_key":true}}`)))
	if forbidden.Code != http.StatusBadRequest {
		t.Fatalf("unknown key status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
}
