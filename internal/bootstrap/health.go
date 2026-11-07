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

// readinessHandler 组合数据库就绪判定、C14 能力快照与审计 Outbox 中间态。
// 可选集成仍由能力开关控制，不会被静默提升为强制就绪条件；
// audit_outbox 只观测投递器状态与积压计数，查询失败标 query_error 但不改变就绪结论，
// 也不新增指标端点或外部依赖。
// 参数：config 为运行配置（决定能力快照）；databaseHealthy 为必需的数据库健康判定；
// auditOutbox 为审计 Outbox 观测状态，nil 时省略该字段。
// 返回值：就绪端点处理器。
func readinessHandler(config Config, databaseHealthy func(context.Context) bool, auditOutbox *auditOutboxState) gin.HandlerFunc {
	return func(c *gin.Context) {
		if databaseHealthy == nil || !databaseHealthy(c.Request.Context()) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		winReady := config.WinConfirmationMode == "contract" || config.WinConfirmationMode == "local_evidence"
		body := gin.H{
			"status": "ready",
			"capabilities": gin.H{
				"win_confirmation":      gin.H{"mode": config.WinConfirmationMode, "state": winReadyState(winReady)},
				"contract_verification": gin.H{"enabled": config.ContractVerificationEnabled, "state": capabilityState(config.ContractVerificationEnabled)},
				"contract_transfer":     gin.H{"subscribed": config.ContractTransferSubscribed, "state": capabilityState(config.ContractTransferSubscribed)},
			},
		}
		if field := auditOutbox.snapshot(c.Request.Context()); field != nil {
			body["audit_outbox"] = field
		}
		c.JSON(http.StatusOK, body)
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
