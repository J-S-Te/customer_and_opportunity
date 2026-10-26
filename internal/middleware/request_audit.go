package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/observability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type RequestAuditOptions struct {
	TenantID, ApplicationCode, EnvironmentCode string
	// TrustedProxies 是唯一允许采信 X-Real-IP / X-Forwarded-For 的直接对端集合。
	// 未配置时只信任回环地址；绝不再按"对端是私网"推断可信。
	TrustedProxies []netip.Prefix
}

type requestAuditStore interface {
	Start(context.Context, requestaudit.Start) error
	Complete(context.Context, string, requestaudit.Completion) error
}

// RequestAudit 在请求进入认证或业务处理器前
// 预留持久审计证据。在内层中间件确定平台主体后，
// 再完成允许列表内的操作摘要。
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
		// 统一审计方案 §2.3：登录地址缺失时保持未知，绝不用"当前请求地址"回填。
		// 回填会把客户端可伪造的转发头写成会话级登录地址，污染此后所有事件。
		// 登录地址只来自会话建立时记录的权威值（principal.LoginIP）。
		// AUD-IP-2026：操作地址每个请求重新解析，且只在直接对端是显式可信代理时
		// 采信转发头；它独立于登录地址存储，不再互相顶替。
		clientIP, ipSource := loginip.Client(c.Request, options.TrustedProxies)
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
		// AUD-IP-2026：业务层拿不到请求；把解析好的来源放进上下文，
		// 业务审计写入时自动补齐，避免业务事件完全没有地址。
		c.Request = c.Request.WithContext(audit.WithMeta(c.Request.Context(), audit.Meta{
			LoginIP: userLoginIP, ClientIP: clientIP, IPSource: ipSource,
		}))
		if err := store.Complete(finalizeCtx, eventID, requestaudit.Completion{
			ActorType: actorType, ActorID: actorID, ActorName: actorName, UserLoginIP: userLoginIP,
			ClientIP: clientIP, IPSource: ipSource,
			Action: "HTTP_" + safeHTTPMethod(c.Request.Method) + " " + route, Route: route,
			HTTPStatus: status, Result: result, ReasonCode: reason, RiskLevel: risk, CompletedAt: completedAt,
		}); err != nil {
			// STARTED 预留记录保持持久化，由分发器
			// 转为中断失败。禁止记录请求内容。
			slog.Default().ErrorContext(finalizeCtx, "finalize request audit", "request_id", request.ID(c.Request.Context()), "error", err)
		}
	}
}

// isProbePath 仅识别基础设施探针端点，包括其
// 挂载前缀形式。名称相似的业务路由仍须审计。
func isProbePath(path string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	return path == "/healthz" || path == "/livez" || path == "/readyz" || strings.HasSuffix(path, "/healthz") || strings.HasSuffix(path, "/livez") || strings.HasSuffix(path, "/readyz")
}
