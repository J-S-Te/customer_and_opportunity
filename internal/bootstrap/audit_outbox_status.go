package bootstrap

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// auditOutboxStatusHandler exposes only tenant-scoped aggregate delivery state
// to users already authorized to read customer audit history. It intentionally
// never performs a remote check and never returns client credentials, tokens,
// payloads, headers, or request bodies.
func auditOutboxStatusHandler(store *requestaudit.Store, config Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.FromContext(c.Request.Context())
		if !ok {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		status, err := store.Status(c.Request.Context(), principal.TenantID)
		if err != nil {
			response.Error(c, err)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		response.OK(c, gin.H{
			"application_code": config.PlatformApplicationCode,
			"environment_code": config.PlatformEnvironmentCode,
			"status":           status,
		})
	}
}
