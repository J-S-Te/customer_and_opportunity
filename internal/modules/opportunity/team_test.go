package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
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

type teamDirectoryStub struct {
	users map[string]ownerdirectory.User
	err   error
}

func (stub teamDirectoryStub) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	return ownerdirectory.Page{}, stub.err
}

func (stub teamDirectoryStub) Validate(context.Context, string, string) error { return stub.err }

func (stub teamDirectoryStub) Resolve(context.Context, []string) (map[string]ownerdirectory.User, error) {
	return stub.users, stub.err
}

func TestTeamMembersMustResolveToActivePlatformUsers(t *testing.T) {
	members := []Member{{UserID: "user-active", Role: MemberRoleTechnicalSupport}}
	service := &Service{owners: teamDirectoryStub{users: map[string]ownerdirectory.User{
		"user-active": {ID: "user-active", DisplayName: "张三", Organizations: []ownerdirectory.Organization{{ID: "org-1", Name: "技术中心", IsPrimary: true}}},
	}}}
	users, err := service.validateTeamUsers(context.Background(), members)
	if err != nil || users["user-active"].DisplayName != "张三" {
		t.Fatalf("users=%#v err=%v", users, err)
	}
	response := teamResponseWithUsers(7, 3, members, users)
	if !response.DirectoryAvailable || response.Members[0].DisplayName != "张三" || response.Members[0].Organizations[0].Name != "技术中心" || response.Members[0].DirectoryStatus != "ACTIVE" {
		t.Fatalf("response=%#v", response)
	}

	service.owners = teamDirectoryStub{users: map[string]ownerdirectory.User{}}
	if _, err = service.validateTeamUsers(context.Background(), members); err != ErrInvalidTeamMember {
		t.Fatalf("missing user error=%v", err)
	}
	service.owners = teamDirectoryStub{err: ownerdirectory.ErrUnavailable}
	if _, err = service.validateTeamUsers(context.Background(), members); !errors.Is(err, ownerdirectory.ErrUnavailable) {
		t.Fatalf("directory failure error=%v", err)
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
