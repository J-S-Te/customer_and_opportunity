package bootstrap

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// livenessHandler 有意不检查任何依赖。只要进程能够响应该端点，就表示进程存活，
// 即使它因为依赖故障必须从服务流量中摘除。
func livenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "alive"})
}

// readinessHandler 支持注入依赖检查，使路由测试不必建立数据库连接。
// 可选集成仍由能力开关控制，不会被静默提升为强制就绪条件。
func readinessHandler(check func(context.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check == nil || !check(c.Request.Context()) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
