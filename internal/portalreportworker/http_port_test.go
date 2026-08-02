package portalreportworker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

func TestProjectClientMatchesPlatformOAuthAndIntegrationContract(t *testing.T) {
	const clientID = "portal-report-worker"
	const clientSecret = "machine-secret"
	var tokenSeen, requestSeen bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			gotID, gotSecret, ok := r.BasicAuth()
			if !ok || gotID != clientID || gotSecret != clientSecret {
				t.Fatalf("unexpected client authentication: %q %t", gotID, ok)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "report.request.write" {
				t.Fatalf("unexpected token form: %v", r.Form)
			}
			if _, exists := r.Form["audience"]; exists {
				t.Fatalf("platform token endpoint rejects audience: %v", r.Form)
			}
			tokenSeen = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "application-token", "token_type": "Bearer", "expires_in": 300})
		case "/api/v1/report-requests":
			if r.Header.Get("Authorization") != "Bearer application-token" || r.Header.Get("Idempotency-Key") != "evt-stable-1" {
				t.Fatalf("unexpected request authorization/idempotency")
			}
			if _, err := time.Parse(time.RFC3339Nano, r.Header.Get("X-Integration-Timestamp")); err != nil || len(r.Header.Get("X-Integration-Nonce")) < 40 {
				t.Fatalf("timestamp/nonce contract missing")
			}
			requestSeen = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"OK","data":{"downstream_request_id":"PS-99"}}`))
		default:
			http.NotFound(w, r)
		}
	})
	cfg := ProjectServiceConfig{TokenURL: "https://identity.test/oauth2/token", ClientID: clientID, ClientSecret: clientSecret, Scope: "report.request.write", RequestURL: "https://project.test/api/v1/report-requests"}
	client := newProjectServiceClientWithTransport(cfg, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		response := &recordingResponseWriter{header: make(http.Header), status: http.StatusOK}
		handler.ServeHTTP(response, req)
		return response.httpResponse(req), nil
	}))
	id, err := client.Submit(context.Background(), report.Outbox{EventID: "evt-stable-1", Payload: []byte(`{"request_id":42}`)})
	if err != nil || id != "PS-99" || !tokenSeen || !requestSeen {
		t.Fatalf("Submit() id=%q err=%v token=%v request=%v", id, err, tokenSeen, requestSeen)
	}
}

func TestProjectClientDoesNotLeakErrorResponseBody(t *testing.T) {
	client := &projectServiceClient{client: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"X-Request-Id": []string{"safe-request-1"}}, Body: io.NopCloser(strings.NewReader("bearer token and customer secret")), Request: req}, nil
	})}, endpoint: "https://project.test/report-requests", now: func() time.Time { return time.Now().UTC() }, nonce: func() (string, error) { return strings.Repeat("n", 43), nil }}
	_, err := client.Submit(context.Background(), report.Outbox{EventID: "evt-1", Payload: []byte(`{}`)})
	if err == nil || strings.Contains(err.Error(), "customer") || strings.Contains(err.Error(), "bearer") || !strings.Contains(err.Error(), "status=502") || !strings.Contains(err.Error(), "request_id=safe-request-1") {
		t.Fatalf("unsafe or incomplete error: %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type recordingResponseWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (w *recordingResponseWriter) Header() http.Header             { return w.header }
func (w *recordingResponseWriter) WriteHeader(status int)          { w.status = status }
func (w *recordingResponseWriter) Write(value []byte) (int, error) { return w.body.Write(value) }
func (w *recordingResponseWriter) httpResponse(req *http.Request) *http.Response {
	return &http.Response{StatusCode: w.status, Header: w.header, Body: io.NopCloser(strings.NewReader(w.body.String())), Request: req}
}
