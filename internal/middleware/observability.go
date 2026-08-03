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

// 为未使用共享 Principal 的认证实现解析日志主体；只能返回服务端已验证的身份数据。
type AccessLogActor func(*gin.Context) (tenantID string, userID string)

// 每个请求只输出一条字段受限的结构化事件；仅记录允许列表，查询串、原始路径、请求头、请求体、
// Cookie、网络地址和 User-Agent 均不读取。
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
	// 即使处理器在 panic 前已提交成功状态，也保留 Recovery 写入的稳定错误码。状态码/字节数描述
	// 实际 Writer 状态，错误码则表明请求内部仍然失败。
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

// 将 panic 转换为受控 JSON 错误，不记录 panic 值或请求元数据；访问日志只按安全允许列表记录
// 结果，避免 Gin 默认头转储泄露秘密。
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
