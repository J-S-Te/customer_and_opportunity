package portalreportworker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// 使用真实监听器验证生产传输及 OAuth 客户端，
// 而不只是以进程内替身替换 RoundTrip。
func TestExternalReportServiceRealHTTPDeliveryAndRetryIdentity(t *testing.T) {
	var tokenCalls, requestCalls atomic.Int32
	var bad atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			id, secret, ok := r.BasicAuth()
			if err := r.ParseForm(); err != nil || !ok || id != "external-report-writer" || secret != "fixture-secret" || r.Form.Get("scope") != "report.request.write" || r.Form.Get("grant_type") != "client_credentials" {
				bad.Store(true)
				w.WriteHeader(400)
				return
			}
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fixture-access","token_type":"Bearer","expires_in":300}`))
		case "/existing-external-service/report-requests":
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("Idempotency-Key") != "external-stable-event" || string(body) != `{"request_id":42,"customer_id":9}` {
				bad.Store(true)
				w.WriteHeader(400)
				return
			}
			if _, err := time.Parse(time.RFC3339Nano, r.Header.Get("X-Integration-Timestamp")); err != nil || len(r.Header.Get("X-Integration-Nonce")) < 40 {
				bad.Store(true)
				w.WriteHeader(400)
				return
			}
			requestCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"downstream_request_id":"EXTERNAL-42"}}`))
		default:
			bad.Store(true)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := newProjectServiceClient(ProjectServiceConfig{TokenURL: server.URL + "/oauth2/token", ClientID: "external-report-writer", ClientSecret: "fixture-secret", Scope: "report.request.write", RequestURL: server.URL + "/existing-external-service/report-requests"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		id, err := client.Submit(context.Background(), report.Outbox{EventID: "external-stable-event", Payload: []byte(`{"request_id":42,"customer_id":9}`)})
		if err != nil || id != "EXTERNAL-42" {
			t.Fatalf("real external endpoint delivery failed: id=%q err=%v", id, err)
		}
	}
	if bad.Load() || requestCalls.Load() != 2 || tokenCalls.Load() != 1 {
		t.Fatalf("real OAuth/request contract mismatch: bad=%v request=%d token=%d", bad.Load(), requestCalls.Load(), tokenCalls.Load())
	}
}

func TestExternalReportServiceRejectsRedirectAndInvalidReceipts(t *testing.T) {
	for name, response := range map[string]string{"trailing_object": `{"data":{"downstream_request_id":"ok"}} {}`, "oversize": strings.Repeat(" ", 1<<20) + `{"data":{"downstream_request_id":"ok"}}`, "missing_identity": `{"data":{}}`, "control_identity": `{"data":{"downstream_request_id":"unsafe\nidentity"}}`} {
		t.Run(name, func(t *testing.T) {
			client := &projectServiceClient{client: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})}, endpoint: "https://external.example/request", now: time.Now, nonce: randomNonce}
			if id, err := client.Submit(context.Background(), report.Outbox{EventID: "fixture", Payload: []byte(`{}`)}); err == nil || id != "" {
				t.Fatalf("invalid response accepted: id=%q err=%v", id, err)
			}
		})
	}
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		_, _ = w.Write([]byte(`{"data":{"downstream_request_id":"wrong"}}`))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fixture","token_type":"Bearer","expires_in":300}`))
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := newProjectServiceClient(ProjectServiceConfig{TokenURL: server.URL + "/token", ClientID: "fixture", ClientSecret: "fixture", Scope: "report.request.write", RequestURL: server.URL + "/request"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Submit(context.Background(), report.Outbox{EventID: "fixture", Payload: []byte(`{}`)}); err == nil || redirected.Load() {
		t.Fatal("integration redirect accepted or credential-bearing request forwarded")
	}
}
