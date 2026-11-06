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

// capabilityState 按 C14 口径渲染能力状态：disabled 属可选未购买/被停用，
// 不计入就绪降级；ready 表示配置校验通过后的运行态。
// 参数：enabled 为该能力开关的运行事实。
// 返回值：ready 或 disabled。
func capabilityState(enabled bool) string {
	if enabled {
		return "ready"
	}
	return "disabled"
}

// winReadyState 渲染赢单确认模式状态：两种已登记模式都算 ready，未登记模式
// 说明配置漂移，返回 not_configured。
// 参数：valid 为模式是否为 contract/local_evidence 之一。
// 返回值：ready 或 not_configured。
func winReadyState(valid bool) string {
	if valid {
		return "ready"
	}
	return "not_configured"
}
