package presale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPApprovalTaskResolverUsesExactScopeAndAuthenticatedApproverQuery(t *testing.T) {
	t.Parallel()
	var scope, rawQuery, nonce string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			if err := request.ParseForm(); err != nil {
				http.Error(writer, "bad token form", http.StatusBadRequest)
				return
			}
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "approval-reader" || secret != "secret" || request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("client_id") != "" {
				http.Error(writer, "bad token request", http.StatusBadRequest)
				return
			}
			scope = request.Form.Get("scope")
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "reader-token", "token_type": "Bearer", "expires_in": 300})
		case "/current-task":
			if request.Header.Get("Authorization") != "Bearer reader-token" || request.Header.Get("X-Tenant-ID") != "" {
				http.Error(writer, "bad auth", http.StatusUnauthorized)
				return
			}
			rawQuery, nonce = request.URL.RawQuery, request.Header.Get("X-Integration-Nonce")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"approval-1","data":{"engine_task_id":"task-7","engine_instance_id":"instance-7","node":2,"approver_id":"lead-1","status":"PENDING"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	resolver, err := NewHTTPApprovalTaskResolver(context.Background(), ApprovalTaskResolverOptions{
		Endpoint: server.URL + "/current-task", TokenURL: server.URL + "/oauth2/token",
		ClientID: "approval-reader", ClientSecret: "secret", Scope: approvalTaskReadScope,
		HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) },
		NonceReader: strings.NewReader(strings.Repeat("n", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := resolver.ResolveCurrentTask(context.Background(), ApprovalTaskQuery{TenantID: "tenant-a", EngineInstanceID: "instance-7", Node: 2, ApproverID: "lead-1"})
	if err != nil || task.EngineTaskID != "task-7" || scope != approvalTaskReadScope || nonce == "" || rawQuery != "approver_id=lead-1&engine_instance_id=instance-7&node=2" {
		t.Fatalf("task=%+v err=%v scope=%q nonce=%q query=%q", task, err, scope, nonce, rawQuery)
	}
}

func TestHTTPApprovalTaskResolverRejectsOverScopeAndMismatchedTask(t *testing.T) {
	t.Parallel()
	if _, err := NewHTTPApprovalTaskResolver(context.Background(), ApprovalTaskResolverOptions{
		Endpoint: "https://approval.example/current-task", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "reader", ClientSecret: "secret", Scope: "presale.approval.task.read presale.approval.write",
	}); err == nil {
		t.Fatal("over-scoped resolver configuration was accepted")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300})
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":"OK","message":"success","request_id":"approval-1","data":{"engine_task_id":"task-7","engine_instance_id":"instance-7","node":2,"approver_id":"another-user","status":"PENDING"}}`))
	}))
	defer server.Close()
	resolver, err := NewHTTPApprovalTaskResolver(context.Background(), ApprovalTaskResolverOptions{Endpoint: server.URL + "/current-task", TokenURL: server.URL + "/oauth2/token", ClientID: "reader", ClientSecret: "secret", Scope: approvalTaskReadScope, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.ResolveCurrentTask(context.Background(), ApprovalTaskQuery{TenantID: "tenant-a", EngineInstanceID: "instance-7", Node: 2, ApproverID: "lead-1"}); !errorsIsDependency(err) {
		t.Fatalf("mismatched task error=%v", err)
	}
}

func TestHTTPApprovalTaskResolverProductionConfigurationRequiresHTTPS(t *testing.T) {
	if _, err := NewHTTPApprovalTaskResolver(context.Background(), ApprovalTaskResolverOptions{
		Endpoint: "http://approval.example/current-task", TokenURL: "https://identity.example/oauth2/token",
		ClientID: "reader", ClientSecret: "secret", Scope: approvalTaskReadScope,
	}); err == nil {
		t.Fatal("clear-text approval task endpoint was accepted")
	}
}

func errorsIsDependency(err error) bool { return err == ErrDependencyUnavailable }
