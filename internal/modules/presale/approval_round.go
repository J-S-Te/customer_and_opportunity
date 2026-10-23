package presale

import "time"

// approvalRoundStart 归一化到与 crm_presale_approval_instances.started_at 相同的毫秒精度。
// 同一轮审批无论进程内还是从数据库读回，都得到相同的轮次起点。
func approvalRoundStart(at time.Time) time.Time {
	return at.UTC().Truncate(time.Millisecond)
}

// approvalRoundDiscriminator 标识一次审批轮次，而不是一次投递重试。
// started_at 在创建审批实例时写入、重开申请时重置；同一轮内的节点重试复用该值，
// 重开/新一轮得到新的 SourceDiscriminator，从而生成新的站内消息。
func approvalRoundDiscriminator(instance *ApprovalInstance) string {
	if instance == nil || instance.StartedAt == nil || instance.StartedAt.IsZero() {
		return ""
	}
	return "approval-round:" + approvalRoundStart(*instance.StartedAt).Format(time.RFC3339Nano)
}
