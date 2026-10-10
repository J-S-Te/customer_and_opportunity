package commercial

import (
	"context"
	core "github.com/J-S-Te/license-core"
	"github.com/J-S-Te/license-core/consumer"
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
	"log/slog"
	"net/http"
	"strings"
)

type Check func(context.Context, core.Operation) error

func Start(ctx context.Context, application string) (Check, error) {
	g, err := consumer.FromEnvironment(application)
	if err != nil {
		return nil, err
	}
	go func() {
		if err := g.Run(ctx, func(error) { slog.Warn("commercial license synchronization unavailable") }); err != nil && ctx.Err() == nil {
			slog.Error("commercial license synchronization stopped")
		}
	}()
	return g.Check, nil
}

func Operation(kind, method, path string) core.Operation {
	switch path {
	case "/livez", "/readyz", "/healthz", "/auth/login", "/auth/callback", "/auth/logout", "/auth/local-logout", "/auth/backchannel-logout", "/api/v1/auth/me", "/api/v1/capabilities", "/api/v1/audit-outbox/status", "/api/v1/account/security", "/api/v1/account/sessions":
		return core.ESSENTIAL_SERVICE
	}
	if kind == "crm" {
		switch method + " " + path {
		case "POST /api/v1/customers/duplicate-check":
			return core.READ_HISTORY
		case "POST /api/v1/customer-exports", "POST /api/v1/presale/reports/exports":
			return core.EXPORT_HISTORY
		case "POST /api/v1/internal/portal/invites/verify", "POST /api/v1/notifications/:id/read", "POST /api/v1/presale/alerts/:id/read":
			return core.ESSENTIAL_SERVICE
		}
	}
	if kind == "portal" {
		switch method + " " + path {
		case "POST /api/v1/projects/:projectID/exports", "POST /api/v1/project-exports/:id/download-grants", "POST /api/v1/project-exports/:id/downloads", "POST /api/v1/reports/:id/download-grants", "POST /api/v1/report-requests/:id/download-grants", "POST /api/v1/reports/:id/downloads", "POST /api/v1/report-requests/:id/downloads", "POST /api/v1/filings/:id/exports":
			return core.EXPORT_HISTORY
		case "DELETE /api/v1/account/sessions/:id", "POST /api/v1/account/security-events/:id/ack", "POST /internal/accounts/disable", "POST /internal/accounts/reconciliation-snapshot", "POST /api/v1/report-notifications/:id/read", "POST /api/v1/feedback-notifications/:id/read", "POST /api/v1/project-conversations/:id/read", "POST /internal/project-conversations/:id/read", "POST /internal/evaluations/:id/low-score-notice/read":
			return core.ESSENTIAL_SERVICE
		}
	}
	if historicalRoutes[kind][method+" "+path] {
		return core.READ_HISTORY
	}
	return core.MUTATE_BUSINESS
}

func Middleware(check Check, kind, prefix string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check == nil {
			c.Next()
			return
		} // Test/embed callers only; production startup always installs a gate.
		path := strings.TrimPrefix(c.FullPath(), strings.TrimRight(prefix, "/"))
		if err := check(c.Request.Context(), Operation(kind, c.Request.Method, path)); err != nil {
			response.Error(c, apperror.New(http.StatusForbidden, "COMMERCIAL_LICENSE_DENIED", "商业授权不允许此操作，请联系管理员"))
			c.Abort()
			return
		}
		c.Next()
	}
}
