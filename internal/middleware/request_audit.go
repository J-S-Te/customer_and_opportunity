package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/observability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type RequestAuditOptions struct {
	TenantID, ApplicationCode, EnvironmentCode string
}

type requestAuditStore interface {
	Start(context.Context, requestaudit.Start) error
	Complete(context.Context, string, requestaudit.Completion) error
}

// RequestAudit reserves durable audit evidence before a request can enter an
// authentication or business handler. It finalizes the allow-listed operation
// summary after all inner middleware has established the platform principal.
func RequestAudit(store requestAuditStore, options RequestAuditOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 健康检查由容器编排周期性调用，来源必然是 Docker 网络地址，
		// 不代表用户行为，也不应写入面向用户的业务审计列表。
		if isProbePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		now := time.Now().UTC()
		eventID := request.NewID()
		if err := store.Start(c.Request.Context(), requestaudit.Start{
			EventID: eventID, TenantID: options.TenantID, ApplicationCode: options.ApplicationCode,
			EnvironmentCode: options.EnvironmentCode, RequestID: request.ID(c.Request.Context()),
			Method: safeHTTPMethod(c.Request.Method), OccurredAt: now,
		}); err != nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "COMMON_AUDIT_UNAVAILABLE", "operation audit is unavailable"))
			c.Abort()
			return
		}

		c.Next()
		completedAt := time.Now().UTC()
		actorType, actorID, actorName, userLoginIP := "SYSTEM", "", "", ""
		if principal, ok := auth.FromContext(c.Request.Context()); ok {
			actorID, actorName = principal.UserID, principal.DisplayName
			userLoginIP = loginip.Normalize(principal.LoginIP)
			actorType = "USER"
			if strings.HasPrefix(principal.UserID, "machine:") {
				actorType = "MACHINE"
			}
		}
		route := c.FullPath()
		if route == "" {
			route = "UNMATCHED"
		}
		status := c.Writer.Status()
		result := "SUCCESS"
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			result = "DENIED"
		} else if status >= http.StatusBadRequest {
			result = "FAILURE"
		}
		risk := "LOW"
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions {
			risk = "HIGH"
		} else if result != "SUCCESS" {
			risk = "MEDIUM"
		}
		reason := observability.ErrorCode(c)
		finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
		defer cancel()
		if err := store.Complete(finalizeCtx, eventID, requestaudit.Completion{
			ActorType: actorType, ActorID: actorID, ActorName: actorName, UserLoginIP: userLoginIP,
			Action: "HTTP_" + safeHTTPMethod(c.Request.Method) + " " + route, Route: route,
			HTTPStatus: status, Result: result, ReasonCode: reason, RiskLevel: risk, CompletedAt: completedAt,
		}); err != nil {
			// The STARTED reservation remains durable and is converted to an
			// interrupted failure by the dispatcher. Never log request content.
			slog.Default().ErrorContext(finalizeCtx, "finalize request audit", "request_id", request.ID(c.Request.Context()), "error", err)
		}
	}
}

func isProbePath(path string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	return path == "/healthz" || path == "/livez" || path == "/readyz" || strings.HasSuffix(path, "/healthz") || strings.HasSuffix(path, "/livez") || strings.HasSuffix(path, "/readyz")
}
