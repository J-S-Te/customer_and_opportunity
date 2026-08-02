package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

func TestAccessLogUsesSafeAllowlistAndRouteTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(
		RequestID(),
		AccessLog(logger, "portal", func(*gin.Context) (string, string) { return "tenant-safe", "user-safe" }),
		Recovery(logger, "portal"),
	)
	router.POST("/objects/:id", func(c *gin.Context) {
		response.Error(c, apperror.New(http.StatusUnprocessableEntity, "TEST_VALIDATION_ERROR", "invalid request"))
	})

	request := httptest.NewRequest(http.MethodPost, "/objects/secret-path?token=secret-query", strings.NewReader(`{"password":"secret-body"}`))
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Authorization", "Bearer secret-access-token")
	request.Header.Set("Cookie", "session=secret-cookie")
	request.Header.Set("User-Agent", "secret-user-agent")
	request.Header.Set("X-Private-Header", "secret-header")
	request.Header.Set(RequestIDHeader, "safe-request-id")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"request_id":"safe-request-id"`) {
		t.Fatalf("error response did not preserve request ID: %s", recorder.Body.String())
	}
	assertNoSensitiveAccessLogValue(t, output.String())
	entry := decodeSingleLogEntry(t, output.String())
	assertExactAccessLogFields(t, entry)
	assertLogField(t, entry, "request_id", "safe-request-id")
	assertLogField(t, entry, "tenant_id", "tenant-safe")
	assertLogField(t, entry, "user_id", "user-safe")
	assertLogField(t, entry, "module", "portal")
	assertLogField(t, entry, "method", http.MethodPost)
	assertLogField(t, entry, "route", "/objects/:id")
	assertLogField(t, entry, "error_code", "TEST_VALIDATION_ERROR")
	if got := int(entry["status"].(float64)); got != http.StatusUnprocessableEntity {
		t.Fatalf("status field = %d", got)
	}
}

func TestRecoveryDoesNotLogPanicOrRequestSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(RequestID(), AccessLog(logger, "crm", nil), Recovery(logger, "crm"))
	router.GET("/panic/:id", func(*gin.Context) { panic("secret-panic-value") })
	request := httptest.NewRequest(http.MethodGet, "/panic/secret-path?code=secret-code", nil)
	request.Header.Set("Authorization", "Bearer secret-access-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	assertNoSensitiveAccessLogValue(t, output.String())
	if strings.Contains(output.String(), "secret-panic-value") || strings.Contains(output.String(), "secret-code") {
		t.Fatalf("panic log exposed a secret: %s", output.String())
	}
	if !strings.Contains(output.String(), `"route":"/panic/:id"`) || !strings.Contains(output.String(), `"error_code":"COMMON_INTERNAL_ERROR"`) {
		t.Fatalf("safe panic access log is incomplete: %s", output.String())
	}
}

func TestAccessLogRecordsActualCommittedResponseWhenHandlerPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(RequestID(), AccessLog(logger, "crm", nil), Recovery(logger, "crm"))
	router.GET("/partial/:id", func(c *gin.Context) {
		c.String(http.StatusAccepted, "accepted")
		panic("secret-panic-value")
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/partial/secret-path", nil))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("committed status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	entry := decodeAccessLogEntry(t, output.String())
	if got := int(entry["status"].(float64)); got != http.StatusAccepted {
		t.Fatalf("logged status = %d, want actual committed %d", got, http.StatusAccepted)
	}
	if got := int(entry["bytes"].(float64)); got != recorder.Body.Len() {
		t.Fatalf("logged bytes = %d, want actual %d", got, recorder.Body.Len())
	}
	assertLogField(t, entry, "error_code", "COMMON_INTERNAL_ERROR")
	assertNoSensitiveAccessLogValue(t, output.String())
}

func TestAccessLogDoesNotCopyUnrecognizedMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(RequestID(), AccessLog(logger, "crm", nil))
	router.NoRoute(func(c *gin.Context) { response.Error(c, apperror.ErrNotFound) })
	request := httptest.NewRequest("SECRET-METHOD", "/objects", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if strings.Contains(output.String(), "SECRET-METHOD") {
		t.Fatalf("access log copied an unrecognized method: %s", output.String())
	}
	assertLogField(t, decodeSingleLogEntry(t, output.String()), "method", "OTHER")
}

func assertNoSensitiveAccessLogValue(t *testing.T, log string) {
	t.Helper()
	for _, forbidden := range []string{
		"secret-path", "secret-query", "secret-body", "secret-access-token",
		"secret-cookie", "secret-user-agent", "secret-header", "192.0.2.10",
		"Authorization", "Cookie", "User-Agent", "X-Private-Header", "password",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("access log contains forbidden value %q: %s", forbidden, log)
		}
	}
}

func decodeSingleLogEntry(t *testing.T, value string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one log entry, got %d: %s", len(lines), value)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	return entry
}

func decodeAccessLogEntry(t *testing.T, value string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "http_request_completed" {
			return entry
		}
	}
	t.Fatalf("access log entry not found: %s", value)
	return nil
}

func assertLogField(t *testing.T, entry map[string]any, key, expected string) {
	t.Helper()
	if actual, _ := entry[key].(string); actual != expected {
		t.Fatalf("%s = %q, want %q", key, actual, expected)
	}
}

func assertExactAccessLogFields(t *testing.T, entry map[string]any) {
	t.Helper()
	allowed := map[string]struct{}{
		"time": {}, "level": {}, "msg": {}, "request_id": {}, "tenant_id": {}, "user_id": {},
		"module": {}, "error_code": {}, "method": {}, "route": {}, "status": {}, "duration_ms": {}, "bytes": {},
	}
	for key := range entry {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("access log contains a non-allowlisted field %q: %#v", key, entry)
		}
	}
	for key := range allowed {
		if _, ok := entry[key]; !ok {
			t.Fatalf("access log is missing required field %q: %#v", key, entry)
		}
	}
}
