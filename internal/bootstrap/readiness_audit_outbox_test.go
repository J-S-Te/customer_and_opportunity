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
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
)

// /readyz 的观测能力必须能由真实存储实现，held 与 pending 同源。
var _ auditOutboxPendingCounter = (*requestaudit.Store)(nil)

type stubAuditOutboxCounter struct {
	pending int64
	held    int64
	err     error
	heldErr error
	calls   int
}

func (s *stubAuditOutboxCounter) PendingCount(context.Context) (int64, error) {
	s.calls++
	return s.pending, s.err
}

func (s *stubAuditOutboxCounter) HeldCount(context.Context) (int64, error) {
	s.calls++
	return s.held, s.heldErr
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
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 12, held: 4}, nil)
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
	if field["held"] != float64(4) {
		t.Fatalf("held=%v want 4", field["held"])
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
	if _, ok := field["held"]; ok {
		t.Fatalf("failed query must not report a held count: %v", field)
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

// RETRY/PROCESSING 的积压同样代表审计事件尚未投递成功。pending 必须把它们算进去，
// 否则投递链路故障时 CRM 的 audit_outbox.pending 会靠近 0，反而掩盖故障。
func TestReadinessReportsRetryAndProcessingBacklogAsPending(t *testing.T) {
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 7}, nil)
	state.MarkRunning()
	status, body := getReadyz(t, readinessRouter(Config{}, func(context.Context) bool { return true }, state))
	if status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	field := auditOutboxField(t, body)
	if field["pending"] != float64(7) || field["pending"] == float64(0) {
		t.Fatalf("retry/processing backlog must surface as non-zero pending: %v", field)
	}
}

// held 为 0 时必须显式输出 0，不能用缺字段伪装成「无挂起行」。
func TestReadinessReportsZeroHeldRowsExplicitly(t *testing.T) {
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 2, held: 0}, nil)
	state.MarkRunning()
	status, body := getReadyz(t, readinessRouter(Config{}, func(context.Context) bool { return true }, state))
	if status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	field := auditOutboxField(t, body)
	held, ok := field["held"]
	if !ok {
		t.Fatalf("readyz must always report held when the query succeeds: %v", field)
	}
	if held != float64(0) {
		t.Fatalf("held=%v want 0", held)
	}
	if _, ok := field["query_error"]; ok {
		t.Fatalf("unexpected query_error: %v", field)
	}
}

// held 查询失败与 pending 同样处理：标 query_error，不伪装 0，也不泄露原始错误。
func TestReadinessMarksQueryErrorWhenHeldCountFails(t *testing.T) {
	state := newAuditOutboxState(&stubAuditOutboxCounter{pending: 5, heldErr: errors.New("database is down")}, nil)
	state.MarkRunning()
	status, body := getReadyz(t, readinessRouter(Config{}, func(context.Context) bool { return true }, state))
	if status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	field := auditOutboxField(t, body)
	if field["query_error"] != true {
		t.Fatalf("query_error=%v want true", field["query_error"])
	}
	if _, ok := field["held"]; ok {
		t.Fatalf("failed held query must not report a count: %v", field)
	}
	if _, ok := field["error"]; ok {
		t.Fatalf("held query failure must not leak the database error: %v", field)
	}
}
