package bootstrap

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// auditOutboxPendingCounter 是就绪端点读取本地审计 Outbox 积压量的最小能力。
// 只取聚合计数，使就绪端点既不接触审计载荷，也不引入新的存储依赖或指标端口。
// PendingCount 是尚未投递完成（PENDING/RETRY/PROCESSING）的积压；
// HeldCount 是其中被人工挂起、Claim 永不认领的行（RETRY 且无下次尝试时间）。
type auditOutboxPendingCounter interface {
	PendingCount(ctx context.Context) (int64, error)
	HeldCount(ctx context.Context) (int64, error)
}

// auditOutboxState 记录请求审计投递器是否在运行，并为 /readyz 生成 audit_outbox 中间态。
//
// 写路径与投递器是两件事：投递器未启动时，请求审计仍写入本地 Outbox，事件不会丢失，
// 只是积压的 PENDING 行只增不减。因此这里的字段只用于观测和一次性 WARN，
// 不参与就绪判定，也不新增 /metrics 暴露面（与平台 B4 同一口径）。
type auditOutboxState struct {
	running atomic.Bool
	counter auditOutboxPendingCounter
	logger  *slog.Logger
	warned  sync.Once
}

// newAuditOutboxState 构造就绪观测状态；counter 可为 nil（此时不查询积压计数）。
// 参数：counter 为 Outbox 积压计数来源；logger 为告警日志器，nil 时使用默认日志器。
func newAuditOutboxState(counter auditOutboxPendingCounter, logger *slog.Logger) *auditOutboxState {
	if logger == nil {
		logger = slog.Default()
	}
	return &auditOutboxState{counter: counter, logger: logger}
}

// MarkRunning 在投递器 goroutine 启动前调用，声明投递器已进入运行态。
func (s *auditOutboxState) MarkRunning() { s.running.Store(true) }

// MarkStopped 在投递器 Run 返回后调用；此后审计写路径仍工作，但积压不再被推进。
func (s *auditOutboxState) MarkStopped() { s.running.Store(false) }

// snapshot 返回 /readyz 的 audit_outbox 字段：
//   - dispatcher：running|disabled，投递器是否在运行；
//   - pending：最新一次未投递完成积压计数（PENDING/RETRY/PROCESSING），仅在查询成功时出现；
//   - held：人工挂起行数（RETRY 且 next_attempt_at 为 NULL，永不重投），仅在查询成功时出现；
//   - query_error：计数查询失败标记，失败不改变就绪结论。
//
// 返回值：字段映射；接收者为 nil 时返回 nil，由调用方省略该字段。
func (s *auditOutboxState) snapshot(ctx context.Context) gin.H {
	if s == nil {
		return nil
	}
	field := gin.H{"dispatcher": "disabled"}
	if s.running.Load() {
		field["dispatcher"] = "running"
	} else {
		// 投递器未运行而写路径仍在工作：事件持续落库、积压只增不减，必须让运维在日志中看到。
		// 只告警一次，避免每次探针都刷日志；就绪保持 ready，因为本地 Outbox 仍保留事件。
		s.warned.Do(func() {
			s.logger.Warn("request audit dispatcher is not running while the audit write path is active",
				"component", "request-audit-dispatcher", "impact", "audit_outbox_pending_grows")
		})
	}
	if s.counter == nil {
		return field
	}
	pending, err := s.counter.PendingCount(ctx)
	if err != nil {
		// 计数查询失败只标记 query_error：数据库健康由独立判定负责，此处不能让
		// 观测字段掩盖或附加就绪条件，也不返回原始错误细节。
		field["query_error"] = true
		return field
	}
	field["pending"] = pending
	held, err := s.counter.HeldCount(ctx)
	if err != nil {
		// 与 pending 同一处理：失败只标 query_error，不伪装 0，也不返回原始错误细节。
		field["query_error"] = true
		return field
	}
	field["held"] = held
	return field
}
