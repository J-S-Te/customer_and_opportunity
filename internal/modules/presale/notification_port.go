package presale

import "context"

type WorkflowNotification struct {
	TenantID, RecipientID, Type, Title, Body, RequestNo string
	RequestID, AssignmentID                             uint64
	ApprovalNode                                        uint8
}
type WorkflowNotificationWriter interface {
	Write(context.Context, WorkflowNotification) error
}
