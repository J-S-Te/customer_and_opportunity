package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type replayRepository struct {
	*GORMRepository
	prior *ChangeIdempotency
}

func (r replayRepository) FindChangeIdempotency(context.Context, string, uint64, string, string, string) (*ChangeIdempotency, error) {
	return r.prior, nil
}

func (r replayRepository) FindChangeIdempotencyForUpdate(context.Context, string, uint64, string, string, string) (*ChangeIdempotency, error) {
	return r.prior, nil
}

func teamTestPrincipal() auth.Principal {
	return auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
}

func TestNormalizeMembersValidatesRoleDuplicateAndLimit(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	members, err := normalizeMembers([]TeamMemberInput{{UserID: " user-a ", Role: "technical_support"}}, teamTestPrincipal(), 7, now)
	if err != nil || len(members) != 1 || members[0].UserID != "user-a" || members[0].Role != MemberRoleTechnicalSupport {
		t.Fatalf("normalized=%#v err=%v", members, err)
	}
	if _, err = normalizeMembers([]TeamMemberInput{{UserID: "user-a", Role: "INVALID"}}, teamTestPrincipal(), 7, now); err != ErrInvalidMemberRole {
		t.Fatalf("invalid role error=%v", err)
	}
	if _, err = normalizeMembers([]TeamMemberInput{{UserID: "user-a", Role: MemberRoleOther}, {UserID: "user-a", Role: MemberRoleSalesSupport}}, teamTestPrincipal(), 7, now); err != ErrDuplicateMember {
		t.Fatalf("duplicate error=%v", err)
	}
	tooMany := make([]TeamMemberInput, 51)
	for index := range tooMany {
		tooMany[index] = TeamMemberInput{UserID: uintString(uint64(index + 1)), Role: MemberRoleOther}
	}
	if _, err = normalizeMembers(tooMany, teamTestPrincipal(), 7, now); err != ErrTeamTooLarge {
		t.Fatalf("limit error=%v", err)
	}
}

func TestSameMemberSetIgnoresOrderButNotRole(t *testing.T) {
	current := []Member{{UserID: "a", Role: MemberRoleSalesSupport}, {UserID: "b", Role: MemberRoleOther}}
	desired := []Member{{UserID: "b", Role: MemberRoleOther}, {UserID: "a", Role: MemberRoleSalesSupport}}
	if !sameMemberSet(current, desired) {
		t.Fatal("same unordered set rejected")
	}
	desired[0].Role = MemberRoleBusinessSupport
	if sameMemberSet(current, desired) {
		t.Fatal("role change treated as same set")
	}
}

func TestOwnerNotificationEventsTargetOldAndNewOwner(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	model := &Opportunity{OpportunityNo: "SJ202608010001", Name: "商机"}
	model.ID = 9
	events, err := ownerNotificationEvents("tenant-a", model, "old", "new", 3, now)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if events[0].EventID == events[1].EventID || events[0].Status != "PENDING" || events[1].Status != "PENDING" {
		t.Fatalf("events not uniquely durable: %#v", events)
	}
	if events[0].EventID != ownerEventID("tenant-a", 9, 3, "PREVIOUS_OWNER") {
		t.Fatal("event id is not stable")
	}
}

func TestOwnerNotificationSameSubjectCreatesNoPersonHandoverEvent(t *testing.T) {
	model := &Opportunity{}
	model.ID = 9
	events, err := ownerNotificationEvents("tenant-a", model, "same", "same", 2, time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestOwnerNotificationPayloadUsesExistingFrontendSectionRoute(t *testing.T) {
	model := &Opportunity{}
	model.ID = 9
	events, err := ownerNotificationEvents("tenant-a", model, "old", "new", 2, time.Now())
	if err != nil || len(events) != 2 || !strings.Contains(string(events[0].Payload), "/customer-opportunity/opportunities?opportunity_id=9") {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestOwnerRequestHashBindsVersionAndReason(t *testing.T) {
	base := ownerRequestHash("owner", "org", 3, "reason")
	if base == ownerRequestHash("owner", "org", 4, "reason") || base == ownerRequestHash("owner", "org", 3, "other") {
		t.Fatal("idempotency request hash failed to bind request semantics")
	}
}

func TestReplayOwnerChangeReturnsCommittedResponseAfterConcurrentError(t *testing.T) {
	committed := Response{ID: 9, OwnerUserID: "owner-new", Version: 4}
	encoded, _ := json.Marshal(committed)
	hash := ownerRequestHash("owner-new", "org", 3, "reason")
	service := &Service{repo: replayRepository{prior: &ChangeIdempotency{RequestHash: hash, ResponseJSON: encoded}}}
	result, err := service.replayOwnerChange(context.Background(), teamTestPrincipal(), 9, "same-key", hash, errors.New("duplicate"))
	if err != nil || result.OwnerUserID != "owner-new" || result.Version != 4 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err = service.replayOwnerChange(context.Background(), teamTestPrincipal(), 9, "same-key", "different", errors.New("duplicate")); err != ErrIdempotencyConflict {
		t.Fatalf("different payload error=%v", err)
	}
}
