package presaleprogressnotificationworker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func validFixture(namespace, kind, recipient string, assignmentID uint64) (presale.OutboxEvent, presale.ProgressNotificationEvent, presale.ProgressLog, presale.PresaleRequest, *presale.Assignment) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	eventID := presale.ProgressNotificationEventID("tenant-a", 7, 9, assignmentID, namespace, recipient, kind)
	payload, _ := json.Marshal(eventPayload{ProgressNotificationEventID: 13})
	outbox := presale.OutboxEvent{EventID: eventID, TenantID: "tenant-a", EventType: presale.ProgressNotificationOutboxEventType, AggregateType: "presale_progress_notification_event", AggregateID: "13", Payload: payload}
	evidence := presale.ProgressNotificationEvent{ID: 13, EventID: eventID, TenantID: "tenant-a", RequestID: 7, ProgressID: 9, AssignmentID: assignmentID, RecipientID: recipient, RecipientNamespace: namespace, RecipientKind: kind, AuthorUserID: "author-user", AuthorPersonID: "author-person", OccurredAt: now}
	progress := presale.ProgressLog{ID: 9, TenantID: "tenant-a", RequestID: 7, AuthorID: "author-user", CreatedAt: now}
	request := presale.PresaleRequest{BaseModel: presale.BaseModel{ID: 7, TenantID: "tenant-a"}, RequestNo: "TS7", ApplicantID: "sales-user"}
	if assignmentID == 0 {
		return outbox, evidence, progress, request, nil
	}
	assignment := &presale.Assignment{BaseModel: presale.BaseModel{ID: assignmentID, TenantID: "tenant-a"}, RequestID: 7, AssigneeID: recipient, AssignedAt: now.Add(-time.Hour), IsCurrent: true}
	return outbox, evidence, progress, request, assignment
}

func TestEvidenceSeparatesUserAndPersonRecipientNamespaces(t *testing.T) {
	outbox, evidence, progress, request, assignment := validFixture(presale.ProgressRecipientUser, presale.ProgressRecipientApplicant, "sales-user", 0)
	if reason := validateEvidence(outbox, evidence, progress, request, assignment); reason != "" {
		t.Fatal(reason)
	}
	outbox, evidence, progress, request, assignment = validFixture(presale.ProgressRecipientPerson, presale.ProgressRecipientAssignee, "peer-person", 11)
	if reason := validateEvidence(outbox, evidence, progress, request, assignment); reason != "" {
		t.Fatal(reason)
	}
	evidence.RecipientNamespace = presale.ProgressRecipientUser
	if reason := validateEvidence(outbox, evidence, progress, request, assignment); !strings.Contains(reason, "mismatch") {
		t.Fatalf("namespace forgery reason=%q", reason)
	}
}

func TestAssigneeEvidenceUsesOccurrenceTimeAssignment(t *testing.T) {
	outbox, evidence, progress, request, assignment := validFixture(presale.ProgressRecipientPerson, presale.ProgressRecipientAssignee, "peer-person", 11)
	ended := evidence.OccurredAt.Add(time.Second)
	assignment.EndedAt = &ended
	assignment.IsCurrent = false
	if reason := validateEvidence(outbox, evidence, progress, request, assignment); reason != "" {
		t.Fatalf("later reassignment removal must not cancel historical notification: %s", reason)
	}
	ended = evidence.OccurredAt
	assignment.EndedAt = &ended
	if reason := validateEvidence(outbox, evidence, progress, request, assignment); !strings.Contains(reason, "assignee") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestPayloadProjectionAndLeaseContract(t *testing.T) {
	outbox, evidence, _, request, _ := validFixture(presale.ProgressRecipientUser, presale.ProgressRecipientApplicant, "sales-user", 0)
	payload, reason, valid := validatePayload(outbox)
	if !valid || reason != "" || payload.ProgressNotificationEventID != 13 {
		t.Fatalf("payload=%#v reason=%q valid=%v", payload, reason, valid)
	}
	message := project(outbox, evidence, request, evidence.OccurredAt)
	if message.Type != notification.TypePresaleProgressApplicant || message.ProgressID != 9 || message.RecipientID != "sales-user" || message.TargetPath != "/customer-opportunity/presale?request_id=7" {
		t.Fatalf("message=%#v", message)
	}
	for _, fragment := range []string{"event_type=?", "LIMIT 1", "FOR UPDATE SKIP LOCKED", "locked_until<?"} {
		if !strings.Contains(claimSQL(), fragment) {
			t.Fatalf("claim SQL missing %q", fragment)
		}
	}
	tokenA, err := claimToken("worker")
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := claimToken("worker")
	if err != nil || tokenA == tokenB || !strings.HasPrefix(tokenA, "worker.") {
		t.Fatalf("tokens=%q,%q error=%v", tokenA, tokenB, err)
	}
	now := time.Now().UTC()
	for attempt := uint8(1); attempt <= 6; attempt++ {
		status, next := failurePlan(now, attempt)
		if status != statusRetryWait || next == nil {
			t.Fatalf("attempt=%d status=%s", attempt, status)
		}
	}
	status, next := failurePlan(now, 7)
	if status != statusDeadLetter || next != nil {
		t.Fatalf("status=%s next=%v", status, next)
	}
}
