package feedback

import "time"

// CustomerFeedback is the only feedback aggregate representation returned to
// a browser session. Persistence IDs, tenant/customer/account identifiers,
// ciphertext, idempotency material and audit actors are deliberately absent.
type CustomerFeedback struct {
	ID                    string     `json:"id"`
	FeedbackNo            string     `json:"feedback_no"`
	ProjectID             string     `json:"project_id,omitempty"`
	Type                  string     `json:"type"`
	Title                 string     `json:"title"`
	Description           string     `json:"description"`
	ExpectedContactMasked string     `json:"expected_contact_masked,omitempty"`
	Status                Status     `json:"status"`
	RejectReason          string     `json:"reject_reason,omitempty"`
	SubmittedAt           time.Time  `json:"submitted_at"`
	FirstResponseDueAt    time.Time  `json:"first_response_due_at"`
	FirstRespondedAt      *time.Time `json:"first_responded_at,omitempty"`
	ResolvedAt            *time.Time `json:"resolved_at,omitempty"`
	ClosedAt              *time.Time `json:"closed_at,omitempty"`
}

type CustomerMessage struct {
	SenderType string    `json:"sender_type"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}

type CustomerStatusEvent struct {
	FromStatus Status    `json:"from_status,omitempty"`
	ToStatus   Status    `json:"to_status"`
	Reason     string    `json:"reason,omitempty"`
	ActorType  string    `json:"actor_type"`
	OccurredAt time.Time `json:"occurred_at"`
}

type CustomerTimeline struct {
	Feedback   CustomerFeedback      `json:"feedback"`
	Messages   []CustomerMessage     `json:"messages"`
	StatusLogs []CustomerStatusEvent `json:"status_logs"`
}

func customerFeedback(value *Feedback) CustomerFeedback {
	return CustomerFeedback{
		ID: value.PublicID, FeedbackNo: value.FeedbackNo, ProjectID: value.ProjectID,
		Type: value.Type, Title: value.Title, Description: value.Description,
		ExpectedContactMasked: value.ExpectedContactMasked, Status: value.Status,
		RejectReason: value.RejectReason, SubmittedAt: value.SubmittedAt,
		FirstResponseDueAt: value.FirstResponseDueAt, FirstRespondedAt: value.FirstRespondedAt,
		ResolvedAt: value.ResolvedAt, ClosedAt: value.ClosedAt,
	}
}

func customerTimeline(value *Feedback, messages []Message, logs []StatusLog) *CustomerTimeline {
	view := &CustomerTimeline{
		Feedback: customerFeedback(value), Messages: make([]CustomerMessage, 0, len(messages)),
		StatusLogs: make([]CustomerStatusEvent, 0, len(logs)),
	}
	for _, message := range messages {
		view.Messages = append(view.Messages, CustomerMessage{SenderType: message.SenderType, Content: message.Content, CreatedAt: message.CreatedAt})
	}
	for _, log := range logs {
		view.StatusLogs = append(view.StatusLogs, CustomerStatusEvent{FromStatus: log.FromStatus, ToStatus: log.ToStatus, Reason: log.Reason, ActorType: log.ActorType, OccurredAt: log.OccurredAt})
	}
	return view
}
