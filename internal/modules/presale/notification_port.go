package presale

import "context"

type WorkflowNotification struct {
	TenantID, RecipientID, Type, Title, Body, RequestNo string
	RequestID, AssignmentID                             uint64
	ApprovalNode                                        uint8
	// SourceDiscriminator 只参与 source_event_id 计算，不写入数据库。
	// 用于同一申请、同一收件人和同一类型在不同指派周期中仍能生成新的站内消息。
	SourceDiscriminator string
}
type WorkflowNotificationWriter interface {
	Write(context.Context, WorkflowNotification) error
}
