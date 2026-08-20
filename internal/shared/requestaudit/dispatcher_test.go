package requestaudit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestDispatcherUsesExactAuditGrantAndValidatesEveryReceipt(t *testing.T) {
	var tokenCalls, ingestCalls int
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/oauth2/token":
			tokenCalls++
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "audit-client" || secret != "audit-secret" {
				t.Fatalf("invalid token authentication")
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != "grant_type=client_credentials&scope=audit.ingest" {
				t.Fatalf("token form=%q", body)
			}
			return jsonResponse(http.StatusOK, `{"access_token":"token","token_type":"Bearer","scope":"audit.ingest","expires_in":300}`), nil
		case "/api/v1/audit/events/batch":
			ingestCalls++
			if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("X-Request-ID") == "" || request.Header.Get("traceparent") == "" {
				t.Fatalf("missing audit ingestion headers")
			}
			var payload struct {
				Events []map[string]any `json:"events"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Events) != 1 || payload.Events[0]["event_id"] != "event-1" || payload.Events[0]["resource_id"] != "customer-7" || payload.Events[0]["user_login_ip"] != "203.0.113.9" {
				t.Fatalf("payload=%+v", payload)
			}
			return jsonResponse(http.StatusAccepted, `{"code":"OK","data":[{"event_id":"event-1","status":"ACCEPTED"}]}`), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})}
	dispatcher, err := NewDispatcher(NewStore(nil), DispatcherOptions{
		PlatformBaseURL: "https://platform.example", ClientID: "audit-client", ClientSecret: "audit-secret",
		ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test", WorkerID: "worker-a",
		PollInterval: time.Second, BatchSize: 100, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	values := []Record{{EventID: "event-1", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test", ActorType: "USER", ActorID: "user-1", UserLoginIP: "203.0.113.9", Action: "customer.UPDATE", ResourceType: "customer", ResourceID: "customer-7", RequestID: "request-1", Method: "BUSINESS", Route: "BUSINESS", Result: "SUCCESS", RiskLevel: "HIGH", OccurredAt: time.Now()}}
	if err := dispatcher.deliver(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || ingestCalls != 1 {
		t.Fatalf("token calls=%d ingest calls=%d", tokenCalls, ingestCalls)
	}
}

func TestDispatcherOmitsEmptyUserLoginIP(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/oauth2/token" {
			return jsonResponse(http.StatusOK, `{"access_token":"token","token_type":"Bearer","scope":"audit.ingest","expires_in":300}`), nil
		}
		var payload struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload.Events[0]["user_login_ip"]; ok {
			t.Fatalf("payload unexpectedly includes user_login_ip: %#v", payload.Events[0])
		}
		return jsonResponse(http.StatusAccepted, `{"code":"OK","data":[{"event_id":"event-1","status":"ACCEPTED"}]}`), nil
	})}
	dispatcher, err := NewDispatcher(NewStore(nil), DispatcherOptions{PlatformBaseURL: "https://platform.example", ClientID: "audit-client", ClientSecret: "audit-secret", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test", WorkerID: "worker-a", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.deliver(context.Background(), []Record{{EventID: "event-1", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test", ActorType: "SYSTEM", Action: "HTTP_GET /healthz", ResourceType: "http_route", RequestID: "request-1", Method: "GET", Route: "/healthz", Result: "SUCCESS", RiskLevel: "LOW", OccurredAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherRejectsMissingReceipt(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/oauth2/token" {
			return jsonResponse(http.StatusOK, `{"access_token":"token","token_type":"Bearer","scope":"audit.ingest","expires_in":300}`), nil
		}
		return jsonResponse(http.StatusAccepted, `{"code":"OK","data":[]}`), nil
	})}
	dispatcher, err := NewDispatcher(NewStore(nil), DispatcherOptions{PlatformBaseURL: "https://platform.example", ClientID: "audit-client", ClientSecret: "audit-secret", ApplicationCode: "customer_portal", EnvironmentCode: "test", WorkerID: "worker-a", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.deliver(context.Background(), []Record{{EventID: "event-1", ApplicationCode: "customer_portal", EnvironmentCode: "test", ActorType: "SYSTEM", Action: "HTTP_GET /healthz", ResourceType: "http_route", RequestID: "request-1", Method: "GET", Route: "/healthz", Result: "SUCCESS", RiskLevel: "LOW", OccurredAt: time.Now()}})
	if err == nil || !strings.Contains(err.Error(), "omitted") {
		t.Fatalf("error=%v", err)
	}
}

func TestDispatcherRejectsOutboxSourceMismatchBeforeRequestingToken(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}
	dispatcher, err := NewDispatcher(NewStore(nil), DispatcherOptions{PlatformBaseURL: "https://platform.example", ClientID: "audit-client", ClientSecret: "audit-secret", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev", WorkerID: "worker-a", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.deliver(context.Background(), []Record{{EventID: "event-1", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "prod"}})
	if !errors.Is(err, errOutboxSourceMismatch) {
		t.Fatalf("error=%v", err)
	}
	if called {
		t.Fatal("source mismatch must not request a token or send an event")
	}
}

func TestDeliveryErrorCodeClassifiesPlatformResponses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "unauthorized", err: platformHTTPError{status: http.StatusUnauthorized}, want: "PLATFORM_AUDIT_UNAUTHORIZED"},
		{name: "forbidden", err: platformHTTPError{status: http.StatusForbidden}, want: "PLATFORM_AUDIT_FORBIDDEN"},
		{name: "binding rejected", err: platformHTTPError{status: http.StatusUnprocessableEntity}, want: "PLATFORM_AUDIT_CLIENT_BINDING_REJECTED"},
		{name: "rate limited", err: platformHTTPError{status: http.StatusTooManyRequests}, want: "PLATFORM_AUDIT_RATE_LIMITED"},
		{name: "server error", err: platformHTTPError{status: http.StatusBadGateway}, want: "PLATFORM_AUDIT_SERVER_ERROR"},
		{name: "source mismatch", err: errOutboxSourceMismatch, want: "PLATFORM_AUDIT_OUTBOX_SOURCE_MISMATCH"},
		{name: "configuration mismatch", err: errAuditConfigurationMismatch, want: "PLATFORM_AUDIT_CONFIGURATION_MISMATCH"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deliveryErrorCode(test.err); got != test.want {
				t.Fatalf("deliveryErrorCode(%v)=%s want %s", test.err, got, test.want)
			}
		})
	}
}

func TestValidateConfigurationConfirmsTrustedPublisherBinding(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/oauth2/token":
			return jsonResponse(http.StatusOK, `{"access_token":"token","token_type":"Bearer","scope":"audit.ingest","expires_in":300}`), nil
		case "/api/v1/audit/ingest/validate":
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("unexpected validation request: method=%s authorization=%q", request.Method, request.Header.Get("Authorization"))
			}
			return jsonResponse(http.StatusOK, `{"code":"OK","data":{"application_code":"customer_and_opportunity","environment_code":"dev","client_id":"customer_and_opportunity-dev-audit-publisher","audit_ingest":true}}`), nil
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
			return nil, nil
		}
	})}
	dispatcher, err := NewDispatcher(NewStore(nil), DispatcherOptions{PlatformBaseURL: "https://platform.example", ClientID: "customer_and_opportunity-dev-audit-publisher", ClientSecret: "audit-secret", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev", WorkerID: "worker-a", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.ValidateConfiguration(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigurationRejectsDifferentEnvironment(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/oauth2/token" {
			return jsonResponse(http.StatusOK, `{"access_token":"token","token_type":"Bearer","scope":"audit.ingest","expires_in":300}`), nil
		}
		return jsonResponse(http.StatusOK, `{"code":"OK","data":{"application_code":"customer_and_opportunity","environment_code":"prod","client_id":"customer_and_opportunity-prod-audit-publisher","audit_ingest":true}}`), nil
	})}
	dispatcher, err := NewDispatcher(NewStore(nil), DispatcherOptions{PlatformBaseURL: "https://platform.example", ClientID: "customer_and_opportunity-prod-audit-publisher", ClientSecret: "audit-secret", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "dev", WorkerID: "worker-a", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.ValidateConfiguration(context.Background())
	if !errors.Is(err, errAuditConfigurationMismatch) {
		t.Fatalf("error=%v", err)
	}
	if got := DeliveryErrorCode(err); got != "PLATFORM_AUDIT_CONFIGURATION_MISMATCH" {
		t.Fatalf("error code=%s", got)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
