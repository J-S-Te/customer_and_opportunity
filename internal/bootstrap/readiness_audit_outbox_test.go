package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubAuditOutboxCounter struct {
	pending int64
	err     error
	calls   int
}

func (s *stubAuditOutboxCounter) PendingCount(context.Context) (int64, error) {
	s.calls++
	return s.pending, s.err
}

func readinessRouter(config Config, healthy func(context.Context) bool, state *auditOutboxState) *gin.Engine {
	router := gin.New()
	router.GET("/readyz", readinessHandler(config, healthy, state))
	return router
}

func getReadyz(t *testing.T, router *gin.Engine) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readyz body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, body
}

func auditOutboxField(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, ok := body["audit_outbox"]
	if !ok {
		t.Fatalf("readyz body has no audit_outbox field: %v", body)
	}
	field, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("audit_outbox is not an object: %T", raw)
	}
	return field
}

func TestReadinessReportsRunningAuditDispatcherWithPendingCount(t *testing.T) {
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 12}, nil)
	state.MarkRunning()
	status, body := getReadyz(t, readinessRouter(Config{}, func(context.Context) bool { return true }, state))
	if status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	field := auditOutboxField(t, body)
	if field["dispatcher"] != "running" {
		t.Fatalf("dispatcher=%v want running", field["dispatcher"])
	}
	if field["pending"] != float64(12) {
		t.Fatalf("pending=%v want 12", field["pending"])
	}
	if _, ok := field["query_error"]; ok {
		t.Fatalf("unexpected query_error: %v", field)
	}
}

func TestReadinessReportsDisabledAuditDispatcherAndWarnsOnce(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 3}, logger)
	router := readinessRouter(Config{}, func(context.Context) bool { return true }, state)

	for i := 0; i < 2; i++ {
		status, body := getReadyz(t, router)
		// 投递器未运行只表示积压不再被推进，写路径仍在工作：必须保持就绪，只作告警。
		if status != http.StatusOK || body["status"] != "ready" {
			t.Fatalf("status=%d body=%v", status, body)
		}
		field := auditOutboxField(t, body)
		if field["dispatcher"] != "disabled" {
			t.Fatalf("dispatcher=%v want disabled", field["dispatcher"])
		}
		if field["pending"] != float64(3) {
			t.Fatalf("pending=%v want 3", field["pending"])
		}
	}
	if got := strings.Count(logs.String(), "request audit dispatcher is not running"); got != 1 {
		t.Fatalf("warn count=%d want 1, logs=%s", got, logs.String())
	}
}

func TestReadinessKeepsReadyWhenAuditOutboxQueryFails(t *testing.T) {
	state := newAuditOutboxState(&stubAuditOutboxCounter{err: errors.New("database is down")}, nil)
	state.MarkRunning()
	status, body := getReadyz(t, readinessRouter(Config{}, func(context.Context) bool { return true }, state))
	if status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	field := auditOutboxField(t, body)
	if field["dispatcher"] != "running" {
		t.Fatalf("dispatcher=%v want running", field["dispatcher"])
	}
	if field["query_error"] != true {
		t.Fatalf("query_error=%v want true", field["query_error"])
	}
	if _, ok := field["pending"]; ok {
		t.Fatalf("failed query must not report a pending count: %v", field)
	}
	if _, ok := field["error"]; ok {
		t.Fatalf("query failure must not leak the database error: %v", field)
	}
}

func TestReadinessStaysUnavailableWithoutAuditOutboxWhenDatabaseUnhealthy(t *testing.T) {
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 1}, nil)
	status, body := getReadyz(t, readinessRouter(Config{}, func(context.Context) bool { return false }, state))
	if status != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if _, ok := body["audit_outbox"]; ok {
		t.Fatalf("not_ready must not carry audit_outbox: %v", body)
	}
}
