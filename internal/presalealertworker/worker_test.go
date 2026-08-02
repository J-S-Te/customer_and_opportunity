package presalealertworker

import (
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func TestEvaluateRuleBoundaries(t *testing.T) {
	t.Parallel()
	entered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	request := scanRequest{Status: presale.StatusPendingApproval, CurrentApprovalNode: 1, StatusEnteredAt: entered, UpdatedAt: entered.Add(23 * time.Hour)}
	rule := presale.AlertRule{Type: presale.AlertApprovalNode1Overdue, ThresholdHours: 24}
	_, due, recipients, active := evaluate(rule, request, nil, entered.Add(24*time.Hour-time.Millisecond))
	if active {
		t.Fatal("alert became active before the UTC due instant")
	}
	if !due.Equal(entered.Add(24 * time.Hour)) {
		t.Fatalf("due=%s", due)
	}
	_, _, _, active = evaluate(rule, request, nil, entered.Add(24*time.Hour))
	if !active {
		t.Fatal("alert must activate exactly at the due instant")
	}
	if len(recipients) != 1 || recipients[0] != (recipientTarget{Kind: recipientRoleScope, ID: "sales_director"}) {
		t.Fatalf("recipients=%v", recipients)
	}
}

func TestExecutionRulesAndRecipientDeduplication(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	request := scanRequest{Status: presale.StatusExecuting, ExpectedEnd: end, ApplicantID: "sales-1"}
	rule := presale.AlertRule{Type: presale.AlertExecutionDueSoon, ThresholdHours: 4}
	basis, due, recipients, active := evaluate(rule, request, []string{"tech-1", "sales-1", "tech-1"}, end.Add(-4*time.Hour))
	if !active || !basis.Equal(end) || !due.Equal(end.Add(-4*time.Hour)) {
		t.Fatalf("evaluation=(%v,%s,%s)", active, basis, due)
	}
	if len(recipients) != 4 || recipients[0] != (recipientTarget{Kind: presale.AlertRecipientPerson, ID: "tech-1"}) || recipients[1] != (recipientTarget{Kind: presale.AlertRecipientPerson, ID: "sales-1"}) || recipients[2] != (recipientTarget{Kind: presale.AlertRecipientUser, ID: "sales-1"}) || recipients[3] != (recipientTarget{Kind: recipientRoleScope, ID: "team_lead"}) {
		t.Fatalf("deduplicated recipients=%v", recipients)
	}
	_, _, _, active = evaluate(rule, request, nil, end)
	if active {
		t.Fatal("due-soon alert must stop at expected_end")
	}
	overdue := presale.AlertRule{Type: presale.AlertExecutionOverdue, ThresholdHours: 2}
	_, due, _, active = evaluate(overdue, request, nil, end.Add(2*time.Hour))
	if !active || !due.Equal(end.Add(2*time.Hour)) {
		t.Fatalf("overdue=(%v,%s)", active, due)
	}
}

func TestDesiredIdentityCancelsRemovedAssigneeOnly(t *testing.T) {
	t.Parallel()
	desired := map[alertIdentity]bool{
		{Type: presale.AlertExecutionOverdue, RuleVersion: 4, RecipientKind: presale.AlertRecipientPerson, RecipientID: "tech-current"}: true,
		{Type: presale.AlertExecutionOverdue, RuleVersion: 4, RecipientKind: presale.AlertRecipientUser, RecipientID: "team-lead"}:      true,
		{Type: presale.AlertExecutionOverdue, RuleVersion: 4, RecipientKind: presale.AlertRecipientUser, RecipientID: "sales-1"}:        true,
	}
	current := alertIdentity{Type: presale.AlertExecutionOverdue, RuleVersion: 4, RecipientKind: presale.AlertRecipientPerson, RecipientID: "tech-current"}
	removed := alertIdentity{Type: presale.AlertExecutionOverdue, RuleVersion: 4, RecipientKind: presale.AlertRecipientPerson, RecipientID: "tech-removed"}
	if !desired[current] {
		t.Fatal("current assignee must retain its alert")
	}
	if desired[removed] {
		t.Fatal("removed assignee must no longer be part of desired alert identity")
	}
}

func TestUniqueRecipientsDeduplicatesAssigneeApplicantAndTeamLead(t *testing.T) {
	t.Parallel()
	values := uniqueRecipients([]recipientTarget{{Kind: presale.AlertRecipientPerson, ID: "shared"}, {Kind: presale.AlertRecipientUser, ID: "shared"}, {Kind: presale.AlertRecipientUser, ID: "lead-1"}, {Kind: presale.AlertRecipientUser, ID: "lead-1"}, {Kind: presale.AlertRecipientUser}})
	if len(values) != 3 || values[0] != (recipientTarget{Kind: presale.AlertRecipientPerson, ID: "shared"}) || values[1] != (recipientTarget{Kind: presale.AlertRecipientUser, ID: "shared"}) || values[2] != (recipientTarget{Kind: presale.AlertRecipientUser, ID: "lead-1"}) {
		t.Fatalf("values=%v", values)
	}
}

func TestManagementRecipientRolesAreAnExplicitCRMOIDCAllowlist(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"sales_director", "team_lead"} {
		if !supportedManagementRecipientRole(role) {
			t.Fatalf("CRM management role %q must resolve from the local OIDC directory", role)
		}
	}
	for _, role := range []string{"technical_director", "project_manager", "implementation_engineer", "", "sales-director"} {
		if supportedManagementRecipientRole(role) {
			t.Fatalf("role %q must not be inferred as a CRM alert recipient role", role)
		}
	}
}

func TestUnrelatedRequestUpdateDoesNotResetTransitionBasis(t *testing.T) {
	t.Parallel()
	entered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	request := scanRequest{Status: presale.StatusApprovedPendingAssignment, StatusEnteredAt: entered, UpdatedAt: entered.Add(47 * time.Hour)}
	rule := presale.AlertRule{Type: presale.AlertAssignmentOverdue, ThresholdHours: 48}
	_, due, _, active := evaluate(rule, request, nil, entered.Add(47*time.Hour))
	if active {
		t.Fatal("unrelated updated_at made alert fire before transition basis deadline")
	}
	if !due.Equal(entered.Add(48 * time.Hour)) {
		t.Fatalf("due=%s", due)
	}
}

func TestNextRequestPageAdvancesCursorAndTerminates(t *testing.T) {
	t.Parallel()
	next, done := nextRequestPage(0, []scanRequest{{ID: 4}, {ID: 9}}, 2)
	if done || next != 9 {
		t.Fatalf("first page next=%d done=%v", next, done)
	}
	next, done = nextRequestPage(next, []scanRequest{{ID: 12}}, 2)
	if !done || next != 12 {
		t.Fatalf("last page next=%d done=%v", next, done)
	}
	next, done = nextRequestPage(next, nil, 2)
	if !done || next != 12 {
		t.Fatalf("empty page next=%d done=%v", next, done)
	}
}

func TestTerminalStatusesAreRecognizedForPendingCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []presale.RequestStatus{presale.StatusCompleted, presale.StatusRejected, presale.StatusCancelled} {
		if !terminal(status) {
			t.Fatalf("status %s was not terminal", status)
		}
	}
	for _, status := range []presale.RequestStatus{presale.StatusPendingApproval, presale.StatusApprovedPendingAssignment, presale.StatusExecuting} {
		if terminal(status) {
			t.Fatalf("status %s was terminal", status)
		}
	}
}

func TestPendingAndUnreadAlertsAreCancelledAfterTerminalTransition(t *testing.T) {
	t.Parallel()
	if !cancellableAlertStatus("PENDING") || !cancellableAlertStatus("UNREAD") {
		t.Fatal("both queued and projected unread alerts must be cancellable")
	}
	for _, status := range []string{"READ", "CANCELLED"} {
		if cancellableAlertStatus(status) {
			t.Fatalf("status %s must retain its historical record", status)
		}
	}
}

func TestAlertIdentityIsStableForDatabaseDeduplication(t *testing.T) {
	t.Parallel()
	// The unique key is tenant/request/type/rule-version/recipient. This pure
	// assertion guards the business identity used by INSERT ... ON CONFLICT.
	identity := func(tenant string, requestID uint64, typ presale.AlertType, version uint64, kind, recipient string) string {
		return tenant + "/" + kind + "/" + recipient + "/" + string(typ) + "/" + time.Unix(int64(requestID), int64(version)).UTC().Format(time.RFC3339Nano)
	}
	first := identity("tenant-a", 7, presale.AlertExecutionOverdue, 3, presale.AlertRecipientPerson, "tech-1")
	if first != identity("tenant-a", 7, presale.AlertExecutionOverdue, 3, presale.AlertRecipientPerson, "tech-1") {
		t.Fatal("same alert scan did not preserve identity")
	}
	if first == identity("tenant-a", 7, presale.AlertExecutionOverdue, 4, presale.AlertRecipientPerson, "tech-1") {
		t.Fatal("new rule version must produce a new identity")
	}
	if first == identity("tenant-a", 7, presale.AlertExecutionOverdue, 3, presale.AlertRecipientUser, "tech-1") {
		t.Fatal("recipient namespace must be part of alert identity")
	}
}
