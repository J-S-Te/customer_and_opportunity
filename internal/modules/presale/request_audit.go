package presale

import (
	"context"
	"fmt"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
)

// writeRequestAudit 为售前申请主数据的创建与修改写入字段级前后值审计。
// 申请没有独立的编辑接口，可编辑字段只在重新提交（reopen）时被整体覆盖：
// 期望开始/结束时间会在多轮“驳回→重开→再提交”中变化，业务表只保留最后一次结果，
// 因此这里和商机、客户模块一样把 before/after 快照写进 crm_audit_events，
// 使“改过几次、每次改成什么”可以从审计里回放。
//
// 两条约束与既有范式一致：
//   - 复用业务事务（txCtx），审计写入失败则整笔业务变更回滚；
//   - 快照来自 RequestView 投影而不是 GORM 模型，联系人只出现掩码值，
//     密文列和幂等键不会进入审计。
//
// 未装配审计写入器（nil）时失败关闭，而不是静默跳过：生产装配必然注入 writer，
// 静默跳过会让“字段变更可追溯”在最需要它的时候悄悄失效。
// 纯幂等重放不写审计，也不会走到这里，因此重试语义不受审计可用性影响。
func (s *Service) writeRequestAudit(ctx context.Context, actor Actor, operation string, id uint64, before, after any, reason string) error {
	if s.auditWriter == nil {
		return ErrAuditUnavailable
	}
	event := audit.Event{
		TenantID: actor.TenantID, Module: "presale", Operation: operation,
		ResourceType: "presale_request", ResourceID: fmt.Sprint(id),
		ActorID: actor.UserID, ActorNameSnapshot: actor.UserName,
		AfterJSON: audit.JSON(after), Reason: strings.TrimSpace(reason), Result: "SUCCESS",
	}
	// 创建没有更新前快照，留空即 NULL，与商机/客户的 CREATE 审计一致。
	if before != nil {
		event.BeforeJSON = audit.JSON(before)
	}
	return s.auditWriter.Write(ctx, event)
}
