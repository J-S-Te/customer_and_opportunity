package ownerchangenotificationworker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
)

func validTestEvent(kind, recipient string) opportunity.OutboxEvent {
	payload := ownerPayload{OpportunityID: 9, OpportunityNo: "SJ202608010001", OpportunityName: "商机", RecipientUserID: recipient, RecipientKind: kind, OwnerUserID: "new", TargetPath: "/opportunities/9", Version: 3}
	encoded, _ := json.Marshal(payload)
	return opportunity.OutboxEvent{EventID: stableOwnerEventID("tenant-a", 9, 3, kind), TenantID: "tenant-a", EventType: ownerChangeEventType, AggregateType: "opportunity", AggregateID: "9", Payload: encoded}
}

func TestValidatePayloadBindsEventIdentityAndRecipientKind(t *testing.T) {
	for _, test := range []struct{ kind, recipient string }{{notification.RecipientPreviousOwner, "old"}, {notification.RecipientNewOwner, "new"}} {
		event := validTestEvent(test.kind, test.recipient)
		payload, reason, ok := validatePayload(event)
		if !ok || reason != "" || payload.RecipientUserID != test.recipient {
			t.Fatalf("payload=%#v reason=%q ok=%v", payload, reason, ok)
		}
	}
	event := validTestEvent(notification.RecipientNewOwner, "new")
	event.EventID = "forged"
	if _, _, ok := validatePayload(event); ok {
		t.Fatal("forged event identity accepted")
	}
	event = validTestEvent(notification.RecipientNewOwner, "new")
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	payload["recipient_kind"] = "MANAGER"
	event.Payload, _ = json.Marshal(payload)
	if _, _, ok := validatePayload(event); ok {
		t.Fatal("unknown recipient kind accepted")
	}
}

func TestProjectionUsesDatabaseSnapshotAndLocalPath(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	event := validTestEvent(notification.RecipientPreviousOwner, "old")
	payload, _, _ := validatePayload(event)
	payload.OpportunityNo, payload.OpportunityName, payload.TargetPath = "forged", "forged", "https://attacker.example"
	message := project(event, payload, opportunityState{ID: 9, OpportunityNo: "SJ-DB", Name: "数据库商机"}, now)
	if message.OpportunityNo != "SJ-DB" || message.OpportunityName != "数据库商机" || message.TargetPath != "/customer-opportunity/opportunities?opportunity_id=9" {
		t.Fatalf("projection trusted payload: %#v", message)
	}
	if message.RecipientKind != notification.RecipientPreviousOwner || !strings.Contains(message.Body, "不再") {
		t.Fatalf("previous-owner semantics lost: %#v", message)
	}
	if message.OpportunityVersion != payload.Version {
		t.Fatalf("notification did not retain handover version: %#v", message)
	}
}

func TestObsoleteUnreadCancellationIsTenantOpportunityAndSourceBound(t *testing.T) {
	where := obsoleteUnreadWhere()
	for _, fragment := range []string{"tenant_id=?", "opportunity_id=?", "type=?", "status=?", "opportunity_version<?", "source_event_id<>?", "deleted_at IS NULL"} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("obsolete cancellation missing %q: %s", fragment, where)
		}
	}
}

func TestAuditProjectionValidatesOldNewAndCancelsSuperseded(t *testing.T) {
	base := ownerPayload{OpportunityID: 9, Version: 3, OwnerUserID: "new"}
	before, after := ownerSnapshot{OwnerUserID: "old", Version: 2}, ownerSnapshot{OwnerUserID: "new", Version: 3}
	current := opportunityState{ID: 9, OwnerUserID: "new", Status: opportunity.StatusFollowing, Version: 4}
	oldPayload := base
	oldPayload.RecipientKind, oldPayload.RecipientUserID = notification.RecipientPreviousOwner, "old"
	if ok, reason := validateAuditProjection(oldPayload, before, after, current, 0); !ok || reason != "" {
		t.Fatalf("valid old owner projection rejected: %q", reason)
	}
	newPayload := base
	newPayload.RecipientKind, newPayload.RecipientUserID = notification.RecipientNewOwner, "new"
	if ok, reason := validateAuditProjection(newPayload, before, after, current, 0); !ok || reason != "" {
		t.Fatalf("valid new owner projection rejected: %q", reason)
	}
	oldPayload.RecipientUserID = "forged"
	if ok, _ := validateAuditProjection(oldPayload, before, after, current, 0); ok {
		t.Fatal("forged previous owner accepted")
	}
	if ok, reason := validateAuditProjection(newPayload, before, after, current, 1); ok || !strings.Contains(reason, "superseded") {
		t.Fatalf("later owner change was not cancelled: ok=%v reason=%q", ok, reason)
	}
	current.OwnerUserID = "newer"
	if ok, _ := validateAuditProjection(newPayload, before, after, current, 0); ok {
		t.Fatal("current owner mismatch accepted")
	}
	current.OwnerUserID, current.Status = "new", opportunity.StatusVoid
	if ok, _ := validateAuditProjection(newPayload, before, after, current, 0); ok {
		t.Fatal("void opportunity accepted")
	}
}

func TestClaimSQLUsesSkipLockedAndRecoversExpiredLease(t *testing.T) {
	sql := claimSQL()
	for _, fragment := range []string{"FOR UPDATE SKIP LOCKED", "status IN (?,?)", "locked_until<?", "event_type=?"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("claim SQL missing %q: %s", fragment, sql)
		}
	}
}

func TestNotificationReplayUsesTenantSourceUniqueIdentity(t *testing.T) {
	clause := notificationConflictClause()
	if !clause.DoNothing || len(clause.Columns) != 2 || clause.Columns[0].Name != "tenant_id" || clause.Columns[1].Name != "source_event_id" {
		t.Fatalf("unsafe replay conflict clause=%#v", clause)
	}
}

func TestFailurePlanRetriesSixTimesThenDeadLetters(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for attempt := uint8(1); attempt <= 6; attempt++ {
		status, next := failurePlan(now, attempt)
		if status != statusRetryWait || next == nil || !next.After(now) {
			t.Fatalf("attempt=%d status=%s next=%v", attempt, status, next)
		}
	}
	status, next := failurePlan(now, 7)
	if status != statusDeadLetter || next != nil {
		t.Fatalf("attempt=7 status=%s next=%v", status, next)
	}
}

func TestStableEventIDMatchesProducerContract(t *testing.T) {
	base := stableOwnerEventID("tenant-a", 9, 3, notification.RecipientNewOwner)
	if len(base) != 64 || base == stableOwnerEventID("tenant-a", 9, 4, notification.RecipientNewOwner) || base == stableOwnerEventID("tenant-a", 9, 3, notification.RecipientPreviousOwner) {
		t.Fatalf("invalid stable identity=%q", base)
	}
}
