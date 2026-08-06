package bootstrap

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// livenessHandler deliberately checks no dependency. A process that can answer
// this endpoint is alive, even when it must be removed from service traffic.
func livenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "alive"})
}

// readinessHandler keeps dependency checks injectable so the route can be
// tested without opening a database connection. Optional integrations remain
// capability-gated and are not silently promoted to mandatory readiness checks.
func readinessHandler(check func(context.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check == nil || !check(c.Request.Context()) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
