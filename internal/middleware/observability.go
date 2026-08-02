package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/observability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// AccessLogActor resolves an authenticated actor that is stored outside the
// shared Principal context. It must only return server-verified identity data.
type AccessLogActor func(*gin.Context) (tenantID string, userID string)

// AccessLog emits one bounded structured event per request. Deliberately only
// allowlisted fields are logged: query strings, raw paths, headers, bodies,
// cookies, network addresses and user agents are never inspected.
func AccessLog(logger *slog.Logger, module string, actor AccessLogActor) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		startedAt := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				emitAccessLog(c, logger, module, actor, startedAt, http.StatusInternalServerError, "COMMON_INTERNAL_ERROR")
				panic(recovered)
			}
			emitAccessLog(c, logger, module, actor, startedAt, c.Writer.Status(), observability.ErrorCode(c))
		}()
		c.Next()
	}
}

func emitAccessLog(c *gin.Context, logger *slog.Logger, module string, actor AccessLogActor, startedAt time.Time, status int, errorCode string) {
	tenantID, userID := "", ""
	if principal, ok := auth.FromContext(c.Request.Context()); ok {
		tenantID, userID = principal.TenantID, principal.UserID
	}
	if actor != nil {
		if resolvedTenant, resolvedUser := actor(c); resolvedTenant != "" || resolvedUser != "" {
			tenantID, userID = resolvedTenant, resolvedUser
		}
	}
	// Keep a stable error code that Recovery recorded even when a handler had
	// already committed a success status before panicking. The status/bytes
	// fields describe the actual writer state; the error code explains that the
	// request still failed internally.
	if status < http.StatusBadRequest && errorCode == "" {
		errorCode = ""
	} else if errorCode == "" {
		switch status {
		case http.StatusNotFound:
			errorCode = "COMMON_NOT_FOUND"
		default:
			errorCode = "COMMON_HTTP_ERROR"
		}
	}
	route := c.FullPath()
	if route == "" {
		route = "UNMATCHED"
	}
	bytesWritten := c.Writer.Size()
	if bytesWritten < 0 {
		bytesWritten = 0
	}
	level := slog.LevelInfo
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	} else if status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}
	logger.LogAttrs(c.Request.Context(), level, "http_request_completed",
		slog.String("request_id", request.ID(c.Request.Context())),
		slog.String("tenant_id", tenantID),
		slog.String("user_id", userID),
		slog.String("module", module),
		slog.String("error_code", errorCode),
		slog.String("method", safeHTTPMethod(c.Request.Method)),
		slog.String("route", route),
		slog.Int("status", status),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
		slog.Int("bytes", bytesWritten),
	)
}

func safeHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions,
		http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

// Recovery returns a controlled JSON error without logging the panic value or
// request metadata. AccessLog records the resulting 500 using only its safe
// allowlist, so secrets cannot leak through Gin's default header dump.
func Recovery(logger *slog.Logger, module string) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() == nil {
				return
			}
			if logger != nil {
				logger.LogAttrs(context.Background(), slog.LevelError, "http_panic_recovered",
					slog.String("request_id", request.ID(c.Request.Context())),
					slog.String("module", module),
				)
			}
			response.Error(c, apperror.New(http.StatusInternalServerError, "COMMON_INTERNAL_ERROR", "internal server error"))
			c.Abort()
		}()
		c.Next()
	}
}
