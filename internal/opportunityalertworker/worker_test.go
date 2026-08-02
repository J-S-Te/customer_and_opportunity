package opportunityalertworker

import (
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
)

func TestTimedStagesContainOnlyFiveAdvancingStages(t *testing.T) {
	t.Parallel()
	values := timedStages()
	if len(values) != 5 {
		t.Fatalf("timed stages=%v", values)
	}
	for _, stage := range []string{opportunity.StageInitial, opportunity.StageRequirement, opportunity.StageSolution, opportunity.StageQuotation, opportunity.StageBid} {
		if !isTimedStage(stage) {
			t.Fatalf("advancing stage %q was excluded", stage)
		}
	}
	for _, stage := range []string{opportunity.StageSigned, opportunity.StageFailed, ""} {
		if isTimedStage(stage) {
			t.Fatalf("terminal/unknown stage %q was timed", stage)
		}
	}
}

func TestAlertDueUsesUTCStageEntryAndHourThreshold(t *testing.T) {
	t.Parallel()
	entered := time.Date(2026, 8, 1, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	due := alertDue(entered, 24)
	want := time.Date(2026, 8, 2, 0, 30, 0, 0, time.UTC)
	if !due.Equal(want) || due.Location() != time.UTC {
		t.Fatalf("due=%s want=%s", due, want)
	}
}

func TestDesiredAlertActivatesAtBoundaryAndBindsCurrentOwner(t *testing.T) {
	t.Parallel()
	entered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	current := scanOpportunity{CurrentStage: opportunity.StageSolution, OwnerUserID: "owner-a", StageChangedAt: entered}
	rule := stageRule{ThresholdHours: 24, ConfigVersion: 7}
	if _, _, active := desiredAlertIdentity(current, rule, entered.Add(24*time.Hour-time.Millisecond)); active {
		t.Fatal("alert activated before threshold")
	}
	identity, due, active := desiredAlertIdentity(current, rule, entered.Add(24*time.Hour))
	if !active || identity.Stage != opportunity.StageSolution || identity.ThresholdVersion != 7 || identity.RecipientID != "owner-a" || !due.Equal(entered.Add(24*time.Hour)) {
		t.Fatalf("identity=%#v due=%s active=%v", identity, due, active)
	}
	current.OwnerUserID = ""
	if _, _, active = desiredAlertIdentity(current, rule, entered.Add(48*time.Hour)); active {
		t.Fatal("missing authoritative owner must fail closed")
	}
}

func TestActiveOpportunityExcludesTerminalVoidAndUnknownStages(t *testing.T) {
	t.Parallel()
	if !activeOpportunity(scanOpportunity{Status: statusFollowing, CurrentStage: opportunity.StageBid}) {
		t.Fatal("active bid opportunity was excluded")
	}
	for _, value := range []scanOpportunity{
		{Status: "CLOSED", CurrentStage: opportunity.StageSigned},
		{Status: "VOID", CurrentStage: opportunity.StageBid},
		{Status: statusFollowing, CurrentStage: opportunity.StageFailed},
	} {
		if activeOpportunity(value) {
			t.Fatalf("inactive opportunity was eligible: %#v", value)
		}
	}
}

func TestStableEventIdentityBindsTenantStageVersionAndRecipient(t *testing.T) {
	t.Parallel()
	base := stableEventID("tenant-a", 9, opportunity.StageQuotation, 3, "owner-a")
	if len(base) > 64 || !strings.HasPrefix(base, "opp-stage-alert-") {
		t.Fatalf("unsafe event id=%q", base)
	}
	if base != stableEventID("tenant-a", 9, opportunity.StageQuotation, 3, "owner-a") {
		t.Fatal("same alert identity generated a different event id")
	}
	for _, changed := range []string{
		stableEventID("tenant-b", 9, opportunity.StageQuotation, 3, "owner-a"),
		stableEventID("tenant-a", 9, opportunity.StageBid, 3, "owner-a"),
		stableEventID("tenant-a", 9, opportunity.StageQuotation, 4, "owner-a"),
		stableEventID("tenant-a", 9, opportunity.StageQuotation, 3, "owner-b"),
	} {
		if base == changed {
			t.Fatal("different alert identity reused event id")
		}
	}
}

func TestNextPageAdvancesDeterministically(t *testing.T) {
	t.Parallel()
	next, done := nextPage(0, []scanOpportunity{{ID: 4}, {ID: 8}}, 2)
	if done || next != 8 {
		t.Fatalf("next=%d done=%v", next, done)
	}
	next, done = nextPage(next, []scanOpportunity{{ID: 11}}, 2)
	if !done || next != 11 {
		t.Fatalf("last next=%d done=%v", next, done)
	}
}

func TestLeaseRenewalBindsOwnerAndRejectsExpiredLease(t *testing.T) {
	t.Parallel()
	sql := leaseRenewSQL()
	for _, predicate := range []string{"owner_id=?", "lease_until>=?", "job_name=?"} {
		if !strings.Contains(sql, predicate) {
			t.Fatalf("lease renewal is missing %q in %q", predicate, sql)
		}
	}
	if err := leaseRenewalAffectedRows(1); err != nil {
		t.Fatalf("owned live lease was rejected: %v", err)
	}
	for _, affected := range []int64{0, 2} {
		if err := leaseRenewalAffectedRows(affected); err != errLeaseLost {
			t.Fatalf("rows=%d error=%v want=%v", affected, err, errLeaseLost)
		}
	}
}
