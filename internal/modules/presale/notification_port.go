package presale

import "context"

type WorkflowNotification struct {
	TenantID, RecipientID, Type, Title, Body, RequestNo string
	RequestID, AssignmentID                             uint64
}
type WorkflowNotificationWriter interface {
	Write(context.Context, WorkflowNotification) error
}
