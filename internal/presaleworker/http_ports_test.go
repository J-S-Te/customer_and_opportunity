package presaleworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func TestOAuthClientMatchesPlatformTokenContract(t *testing.T) {
	const clientID = "presale-worker"
	const clientSecret = "worker-secret"
	var tokenFormSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			gotID, gotSecret, ok := r.BasicAuth()
			if !ok || gotID != clientID || gotSecret != clientSecret {
				t.Fatalf("unexpected client authentication: %q %t", gotID, ok)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "presale.worklog.write" {
				t.Fatalf("unexpected token form: %v", r.Form)
			}
			if _, exists := r.Form["audience"]; exists {
				t.Fatalf("platform token endpoint rejects audience: %v", r.Form)
			}
			tokenFormSeen = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "application-token", "token_type": "Bearer", "expires_in": 300})
		case "/worklogs":
			if r.Header.Get("Authorization") != "Bearer application-token" {
				t.Fatalf("unexpected authorization header")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"OK","message":"accepted","request_id":"pms-request-1","data":{"worklog_id":"WL1","receipt_code":"PMS-ACCEPTED"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := oauthClient(HTTPPortConfig{TokenURL: server.URL + "/oauth2/token", ClientID: clientID, ClientSecret: clientSecret, Scope: "presale.worklog.write"})
	if err != nil {
		t.Fatal(err)
	}
	ports := testHTTPPorts(server.Client())
	ports.pmsClient, ports.pms.PublishURL = client, server.URL+"/worklogs"
	if _, err = ports.PublishWorklog(context.Background(), presale.OutboxEvent{EventID: "evt-platform-contract", Payload: []byte(`{"worklogId":"WL1","idempotencyKey":"WL1"}`)}); err != nil {
		t.Fatal(err)
	}
	if !tokenFormSeen {
		t.Fatal("token endpoint was not called")
	}
}

func TestPublishWorklogSendsStableIdempotencyKey(t *testing.T) {
	var gotKey, gotTimestamp, gotNonce string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		gotTimestamp = r.Header.Get("X-Integration-Timestamp")
		gotNonce = r.Header.Get("X-Integration-Nonce")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"OK","message":"accepted","request_id":"pms-request-1","data":{"worklog_id":"WL1","receipt_code":"PMS-ACCEPTED"}}`))
	}))
	defer server.Close()
	ports := testHTTPPorts(server.Client())
	ports.pms.PublishURL = server.URL
	code, err := ports.PublishWorklog(context.Background(), presale.OutboxEvent{EventID: "event-stable-1", Payload: []byte(`{"worklogId":"WL1","idempotencyKey":"WL1"}`)})
	if err != nil || code != "PMS-ACCEPTED" {
		t.Fatalf("PublishWorklog() code=%q err=%v", code, err)
	}
	if gotKey != "WL1" {
		t.Fatalf("Idempotency-Key=%q", gotKey)
	}
	if _, err = time.Parse(time.RFC3339Nano, gotTimestamp); err != nil {
		t.Fatalf("timestamp=%q err=%v", gotTimestamp, err)
	}
	if raw, decodeErr := base64.RawURLEncoding.DecodeString(gotNonce); decodeErr != nil || len(raw) != 32 {
		t.Fatalf("nonce=%q bytes=%d err=%v", gotNonce, len(raw), decodeErr)
	}
}

func TestPublishWorklogRejectsDriftingBusinessIdempotencyKey(t *testing.T) {
	ports := testHTTPPorts(&http.Client{})
	ports.pms.PublishURL = "https://pms.example/worklogs"
	if _, err := ports.PublishWorklog(context.Background(), presale.OutboxEvent{EventID: "event-1", Payload: []byte(`{"worklogId":"WL1","idempotencyKey":"OTHER"}`)}); err == nil {
		t.Fatal("drifting payload idempotency key was accepted")
	}
}

func TestPostJSONRejectsRedirectAndNonJSONSuccess(t *testing.T) {
	var redirected bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
		case "/target":
			redirected = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"OK","message":"accepted","request_id":"pms-1","data":{"worklog_id":"WL1","receipt_code":"R1"}}`))
		case "/non-json":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`{"code":"OK"}`))
		}
	}))
	defer server.Close()
	client := integrationClient(server.Client().Transport, 5*time.Second)
	ports := testHTTPPorts(client)
	for _, path := range []string{"/redirect", "/non-json"} {
		ports.pms.PublishURL = server.URL + path
		if _, err := ports.PublishWorklog(context.Background(), presale.OutboxEvent{EventID: "event-1", Payload: []byte(`{"worklogId":"WL1","idempotencyKey":"WL1"}`)}); err == nil {
			t.Fatalf("unsafe response %s accepted", path)
		}
	}
	if redirected {
		t.Fatal("integration client followed a redirect")
	}
}

func TestApprovalActionRequiresExplicitAcceptedEnvelopeAndTaskBinding(t *testing.T) {
	tests := []struct {
		name, body string
		wantErr    bool
	}{
		{"accepted", `{"code":"OK","message":"accepted","request_id":"approval-request-1","data":{"engine_task_id":"task-7","accepted":true}}`, false},
		{"business failure", `{"code":"FAILED","message":"rejected","request_id":"approval-request-1","data":{"engine_task_id":"task-7","accepted":false}}`, true},
		{"wrong task", `{"code":"OK","message":"accepted","request_id":"approval-request-1","data":{"engine_task_id":"task-8","accepted":true}}`, true},
		{"empty", `{}`, true},
		{"trailing", `{"code":"OK","message":"accepted","request_id":"approval-request-1","data":{"engine_task_id":"task-7","accepted":true}} {}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Idempotency-Key") != "event-7" || r.Header.Get("X-Integration-Timestamp") == "" || r.Header.Get("X-Integration-Nonce") == "" {
					t.Fatalf("required integration headers are missing")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			ports := testHTTPPorts(server.Client())
			ports.approval.ActionURL = server.URL
			err := ports.Act(context.Background(), presale.OutboxEvent{EventID: "event-7", Payload: []byte(`{"engine_task_id":"task-7"}`)})
			if (err != nil) != test.wantErr {
				t.Fatalf("Act() err=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestPublishWorklogRejectsIncompleteOrDriftingSuccessEnvelope(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"code":"OK","message":"accepted","request_id":"pms-request-1","data":{"worklog_id":"WL1","receipt_code":""}}`,
		`{"code":"OK","message":"accepted","request_id":"pms-request-1","data":{"worklog_id":"OTHER","receipt_code":"PMS-1"}}`,
		`{"code":"OK","message":"accepted","request_id":"pms-request-1","data":{"worklog_id":"WL1","receipt_code":"PMS-1","unknown":true}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		ports := testHTTPPorts(server.Client())
		ports.pms.PublishURL = server.URL
		if _, err := ports.PublishWorklog(context.Background(), presale.OutboxEvent{EventID: "event-1", Payload: []byte(`{"worklogId":"WL1","idempotencyKey":"WL1"}`)}); err == nil {
			server.Close()
			t.Fatalf("body %s was accepted", body)
		}
		server.Close()
	}
}

func TestSafeTransportErrorDoesNotExposeOAuthResponse(t *testing.T) {
	err := safeTransportError(errors.New(`oauth2: cannot fetch token: 400 {"access_token":"secret"}`))
	if strings.Contains(err.Error(), "secret") || err.Error() != "integration transport failed" {
		t.Fatalf("transport details leaked: %v", err)
	}
}

func TestHTTPFailureDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "safe-request-1")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("customer secret and bearer token"))
	}))
	defer server.Close()
	ports := testHTTPPorts(server.Client())
	ports.pms.PublishURL = server.URL
	_, err := ports.PublishWorklog(context.Background(), presale.OutboxEvent{EventID: "event-1", Payload: []byte(`{"worklogId":"WL1","idempotencyKey":"WL1"}`)})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bearer") {
		t.Fatalf("response body leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "status=502") || !strings.Contains(err.Error(), "request_id=safe-request-1") {
		t.Fatalf("safe diagnostics missing: %v", err)
	}
}

func testHTTPPorts(client *http.Client) *httpPorts {
	return &httpPorts{
		approvalClient: client,
		pmsClient:      client,
		now:            func() time.Time { return time.Date(2026, 8, 1, 1, 2, 3, 4, time.UTC) },
		nonceReader:    strings.NewReader(strings.Repeat("n", 32*16)),
	}
}
