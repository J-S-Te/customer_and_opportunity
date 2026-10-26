package presale

import (
	"context"
	"strings"
	"time"
)

func (s *Service) recordAssignmentEvent(ctx context.Context, actor Actor, request *PresaleRequest, assignment *Assignment, eventType, reason string, occurredAt time.Time) (*AssignmentEvent, error) {
	if request == nil || assignment == nil || assignment.ID == 0 || strings.TrimSpace(assignment.AssigneeID) == "" ||
		(eventType != AssignmentEventAdded && eventType != AssignmentEventRemoved) {
		return nil, ErrInvalidInput
	}
	eventID := AssignmentNotificationEventID(actor.TenantID, request.ID, assignment.ID, eventType)
	evidence := &AssignmentEvent{
		EventID: eventID, TenantID: actor.TenantID, RequestID: request.ID, AssignmentID: assignment.ID,
		EventType: eventType, RecipientPersonID: assignment.AssigneeID,
		RecipientNamespace: assignment.AssigneeNamespace,
		PersonNameSnapshot: assignment.AssigneeNameSnapshot, RoleSnapshot: assignment.AssigneeRole,
		ChangeReason: reason, ActorID: actor.UserID, RequestIDTrace: actor.RequestID, OccurredAt: occurredAt,
	}
	if err := s.repo.CreateAssignmentEvent(ctx, evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}
