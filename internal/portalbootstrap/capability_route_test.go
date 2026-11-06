package portalbootstrap

import (
	"context"
	"encoding/json"
	"errors"
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

// customerDirectoryStub 记录 capability 写入前校验用的 tenant+customer，
// 使 handler 级用例可以在无数据库的情况下断言“写前校验客户存在”。
type customerDirectoryStub struct {
	exists    bool
	err       error
	calls     int
	tenantIDs []string
	customers []uint64
}

func (s *customerDirectoryStub) Exists(_ context.Context, tenantID string, customerID uint64) (bool, error) {
	s.calls++
	s.tenantIDs = append(s.tenantIDs, tenantID)
	s.customers = append(s.customers, customerID)
	if s.err != nil {
		return false, s.err
	}
	return s.exists, nil
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
		Config: config, CustomerCapabilities: store, CustomerDirectory: &customerDirectoryStub{exists: true},
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

// 写入前必须证明 tenant+customer 是可用客户：不存在返回既有 not-found，
// 目录不可用返回 503，两种失败都不能落库。
func TestCapabilityUpdateValidatesCustomerBeforeUpsert(t *testing.T) {
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example"}
	authenticator := fakeMachineAuthenticator{principal: sharedauth.Principal{TenantID: "tenant-a", Permissions: map[string]struct{}{"portal.customer_capabilities.manage": {}}}}
	for _, test := range []struct {
		name       string
		directory  CustomerDirectory
		status     int
		upsertCall int
		lookups    int
	}{
		{name: "missing customer", directory: &customerDirectoryStub{exists: false}, status: http.StatusNotFound, upsertCall: 0, lookups: 1},
		{name: "directory unavailable", directory: &customerDirectoryStub{err: errors.New("directory down")}, status: http.StatusServiceUnavailable, upsertCall: 0, lookups: 1},
		{name: "directory not wired", directory: nil, status: http.StatusServiceUnavailable, upsertCall: 0, lookups: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &capabilityStub{options: capability.DefaultOptions()}
			router := NewRouter(RouterDependencies{
				Config: config, CustomerCapabilities: store, CustomerDirectory: test.directory, MachineAuthenticator: authenticator,
			})
			request := httptest.NewRequest(http.MethodPut, "/customer-portal/internal/customers/7/capabilities", strings.NewReader(`{"capabilities":{"report_enabled":false}}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want %d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			if store.upsertCalls != test.upsertCall {
				t.Fatalf("upsertCalls=%d want %d", store.upsertCalls, test.upsertCall)
			}
			if directory, ok := test.directory.(*customerDirectoryStub); ok {
				if directory.calls != test.lookups {
					t.Fatalf("lookups=%d want %d", directory.calls, test.lookups)
				}
				if directory.calls == 1 && (directory.tenantIDs[0] != "tenant-a" || directory.customers[0] != 7) {
					t.Fatalf("lookup must bind principal tenant and path customer: %q/%d", directory.tenantIDs[0], directory.customers[0])
				}
			}
		})
	}
}
