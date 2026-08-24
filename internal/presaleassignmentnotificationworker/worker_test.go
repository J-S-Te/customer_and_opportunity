package presaleassignmentnotificationworker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func validEvent() presale.OutboxEvent {
	payload, _ := json.Marshal(eventPayload{AssignmentEventID: 13})
	return presale.OutboxEvent{EventID: presale.AssignmentNotificationEventID("tenant-a", 7, 9, presale.AssignmentEventAdded), TenantID: "tenant-a", EventType: eventType, AggregateType: "presale_assignment_event", AggregateID: "13", Payload: payload}
}

func TestPayloadAcceptsOnlyOpaqueEvidenceReference(t *testing.T) {
	event := validEvent()
	payload, reason, ok := validatePayload(event)
	if !ok || reason != "" || payload.AssignmentEventID != 13 {
		t.Fatalf("payload=%#v reason=%q ok=%v", payload, reason, ok)
	}
	event.AggregateID = "14"
	if _, _, ok = validatePayload(event); ok {
		t.Fatal("aggregate mismatch accepted")
	}
	event = validEvent()
	event.Payload = []byte(`{"assignment_event_id":13,"recipient_person_id":"forged"}`)
	if payload, _, ok = validatePayload(event); !ok || payload.AssignmentEventID != 13 {
		t.Fatal("worker should ignore non-authoritative payload display fields")
	}
}

func TestEvidenceBindsTenantRequestAssignmentRecipientAndTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	event := validEvent()
	evidence := presale.AssignmentEvent{EventID: event.EventID, TenantID: "tenant-a", RequestID: 7, AssignmentID: 9, EventType: presale.AssignmentEventAdded, RecipientPersonID: "person-a", PersonNameSnapshot: "工程师", RoleSnapshot: "implementation_engineer", ChangeReason: "首次指派", ActorID: "lead", OccurredAt: now}
	assignment := presale.Assignment{BaseModel: presale.BaseModel{ID: 9, TenantID: "tenant-a"}, RequestID: 7, AssigneeID: "person-a", AssigneeNameSnapshot: "工程师", AssigneeRole: "implementation_engineer", AssignedBy: "lead", AssignedAt: now, ChangeReason: "首次指派", IsCurrent: true}
	request := presale.PresaleRequest{BaseModel: presale.BaseModel{ID: 7, TenantID: "tenant-a"}}
	if reason := validateEvidence(event, evidence, assignment, request); reason != "" {
		t.Fatal(reason)
	}
	assignment.AssigneeID = "forged"
	if reason := validateEvidence(event, evidence, assignment, request); !strings.Contains(reason, "mismatch") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestRemovalEvidenceRequiresEndedAuthoritativeAssignment(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	event := validEvent()
	event.EventID = presale.AssignmentNotificationEventID("tenant-a", 7, 9, presale.AssignmentEventRemoved)
	evidence := presale.AssignmentEvent{EventID: event.EventID, TenantID: "tenant-a", RequestID: 7, AssignmentID: 9, EventType: presale.AssignmentEventRemoved, RecipientPersonID: "person-a", PersonNameSnapshot: "工程师", RoleSnapshot: "implementation_engineer", ActorID: "lead", OccurredAt: now}
	assignment := presale.Assignment{BaseModel: presale.BaseModel{ID: 9, TenantID: "tenant-a", UpdatedBy: "lead"}, RequestID: 7, AssigneeID: "person-a", AssigneeNameSnapshot: "工程师", AssigneeRole: "implementation_engineer", EndedAt: &now, IsCurrent: false}
	request := presale.PresaleRequest{BaseModel: presale.BaseModel{ID: 7, TenantID: "tenant-a"}}
	if reason := validateEvidence(event, evidence, assignment, request); reason != "" {
		t.Fatal(reason)
	}
	assignment.IsCurrent = true
	if reason := validateEvidence(event, evidence, assignment, request); !strings.Contains(reason, "state") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestProjectionUsesDatabaseEvidenceAndSafeLocalPath(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	event := validEvent()
	evidence := presale.AssignmentEvent{AssignmentID: 9, EventType: presale.AssignmentEventAdded, RecipientPersonID: "person-a"}
	request := presale.PresaleRequest{BaseModel: presale.BaseModel{ID: 7}, RequestNo: "TS202608010001", OpportunityID: 3, OpportunityNoSnapshot: "SJ3"}
	message := project(event, evidence, request, now)
	if message.Type != notification.TypePresaleAssigneeAdded || message.RecipientID != "person-a" || message.RequestID != 7 || message.TargetPath != "/customer-opportunity/presale?request_id=7" {
		t.Fatalf("message=%#v", message)
	}
	evidence.EventType = presale.AssignmentEventRemoved
	message = project(event, evidence, request, now)
	if message.Type != notification.TypePresaleAssigneeRemoved || message.RecipientKind != notification.RecipientAssigneeRemoved {
		t.Fatalf("message=%#v", message)
	}
}

func TestAssignedPersonReceivesUnreadInboxNotification(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	event := validEvent()
	evidence := presale.AssignmentEvent{
		AssignmentID: 9, EventType: presale.AssignmentEventAdded,
		RecipientPersonID: "assigned-person-1",
	}
	request := presale.PresaleRequest{
		BaseModel: presale.BaseModel{ID: 7}, RequestNo: "TS202608240001",
		OpportunityID: 3, OpportunityNoSnapshot: "SJ202608240001",
	}

	message := project(event, evidence, request, now)

	if message.Type != notification.TypePresaleAssigneeAdded ||
		message.RecipientID != "assigned-person-1" ||
		message.RecipientKind != notification.RecipientAssigneeAdded ||
		message.Status != notification.StatusUnread {
		t.Fatalf("assigned person inbox routing=%#v", message)
	}
	if message.RequestID != request.ID || message.RequestNo != request.RequestNo || message.AssignmentID != evidence.AssignmentID {
		t.Fatalf("assigned person notification business reference=%#v", message)
	}
	if message.Title == "" || message.Body == "" || message.TargetPath != "/customer-opportunity/presale?request_id=7" {
		t.Fatalf("assigned person notification presentation=%#v", message)
	}
}

func TestClaimAndRetryContract(t *testing.T) {
	for _, fragment := range []string{"FOR UPDATE SKIP LOCKED", "event_type=?", "locked_until<?"} {
		if !strings.Contains(claimSQL(), fragment) {
			t.Fatalf("missing %q", fragment)
		}
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

func TestStableEventIdentityMatchesProducerContract(t *testing.T) {
	value := presale.AssignmentNotificationEventID("tenant-a", 7, 9, presale.AssignmentEventAdded)
	if len(value) != 64 || value == presale.AssignmentNotificationEventID("tenant-a", 7, 9, presale.AssignmentEventRemoved) {
		t.Fatalf("identity=%q", value)
	}
}
