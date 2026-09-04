package presale

import (
	"context"
	"errors"
	"testing"
	"time"
)

type createSecurityRepository struct {
	Repository
	old              *PresaleRequest
	findCalls        int
	missBeforeWinner int
	transactionErr   error
}

func (r *createSecurityRepository) FindRequestByCreateKey(context.Context, string, string) (*PresaleRequest, error) {
	r.findCalls++
	if r.findCalls <= r.missBeforeWinner || r.old == nil {
		return nil, ErrNotFound
	}
	value := *r.old
	return &value, nil
}

func (r *createSecurityRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	if r.transactionErr != nil {
		return r.transactionErr
	}
	return fn(ctx)
}

type accessibleOpportunityReader struct {
	values         map[uint64]OpportunitySnapshot
	calls          int
	err            error
	eligibilityErr error
}

func (r *accessibleOpportunityReader) EnsurePresaleEligible(_ context.Context, _ Actor, id uint64) error {
	if r.eligibilityErr != nil {
		return r.eligibilityErr
	}
	if _, ok := r.values[id]; !ok {
		return ErrNotFound
	}
	return nil
}

func (r *accessibleOpportunityReader) GetAccessible(_ context.Context, _ Actor, id uint64) (OpportunitySnapshot, error) {
	r.calls++
	if r.err != nil {
		return OpportunitySnapshot{}, r.err
	}
	value, ok := r.values[id]
	if !ok {
		return OpportunitySnapshot{}, ErrNotFound
	}
	return value, nil
}

type testPhoneProtector struct{}

func (testPhoneProtector) Encrypt(context.Context, string) ([]byte, error) {
	return []byte("cipher"), nil
}
func (testPhoneProtector) Decrypt(context.Context, []byte) (string, error) {
	return "13800000000", nil
}
func (testPhoneProtector) Mask(string) string { return "138****0000" }

type workerReadinessStub struct {
	ready bool
	err   error
	calls int
}

func (s *workerReadinessStub) HasFreshHeartbeat(context.Context, string, time.Time) (bool, error) {
	s.calls++
	return s.ready, s.err
}

func readyCreateService(repo Repository, opportunities OpportunityReader) *Service {
	return NewService(repo, opportunities, testPhoneProtector{}, fixedClock{}, fixedIDs{}).
		UseWorkerReadiness(&workerReadinessStub{ready: true}, 15*time.Second)
}

func validCreateSecurityInput(opportunityID uint64) CreateRequestInput {
	start := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	return CreateRequestInput{
		OpportunityID: opportunityID, Venue: VenueRemote, ContactName: "customer", ContactPhone: "13800000000",
		Description: "a sufficiently long description", ExpectedStart: start, ExpectedEnd: start.Add(time.Hour), Urgency: UrgencyNormal,
	}
}

func TestCreateRequestReplayIsBoundToAuthorizedActorAndOpportunity(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "sales-a", UserName: "Sales A", Permissions: map[string]bool{"presale.create": true}}
	input := validCreateSecurityInput(7)
	hash, _, err := createRequestHashes(actor, input)
	if err != nil {
		t.Fatal(err)
	}
	old := &PresaleRequest{BaseModel: BaseModel{ID: 91, TenantID: actor.TenantID, CreatedBy: actor.UserID}, OpportunityID: 7, ApplicantID: actor.UserID, CreateIdempotencyKey: "same-key", CreateRequestHash: hash}
	opportunities := &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}, 8: {ID: 8, OpportunityNo: "OP8"}}}

	t.Run("same actor and parent safely replays", func(t *testing.T) {
		repo := &createSecurityRepository{old: old}
		service := readyCreateService(repo, opportunities)
		got, replayErr := service.CreateRequest(context.Background(), actor, "same-key", input)
		if replayErr != nil || got == nil || got.ID != old.ID {
			t.Fatalf("replay=%+v error=%v", got, replayErr)
		}
	})

	for _, test := range []struct {
		name  string
		actor Actor
		input CreateRequestInput
	}{
		{name: "different actor", actor: Actor{TenantID: "tenant-1", UserID: "sales-b", Permissions: map[string]bool{"presale.create": true}}, input: input},
		{name: "different opportunity", actor: actor, input: validCreateSecurityInput(8)},
		{name: "different payload", actor: actor, input: func() CreateRequestInput {
			changed := input
			changed.Description = "a different sufficiently long description"
			return changed
		}()},
	} {
		t.Run(test.name+" conflicts without returning old resource", func(t *testing.T) {
			repo := &createSecurityRepository{old: old}
			service := readyCreateService(repo, opportunities)
			got, replayErr := service.CreateRequest(context.Background(), test.actor, "same-key", test.input)
			if got != nil || !errors.Is(replayErr, ErrIdempotencyConflict) {
				t.Fatalf("resource=%+v error=%v", got, replayErr)
			}
		})
	}
}

func TestCreateRequestRejectsIneligibleOpportunityBeforePersistence(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "sales-a", UserName: "Sales A", Permissions: map[string]bool{"presale.create": true}}
	repository := &createSecurityRepository{}
	opportunities := &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}, eligibilityErr: ErrOpportunityNotEligible}
	service := readyCreateService(repository, opportunities)

	result, err := service.CreateRequest(context.Background(), actor, "new-key", validCreateSecurityInput(7))
	if result != nil || !errors.Is(err, ErrOpportunityNotEligible) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if repository.findCalls < 2 {
		t.Fatalf("expected replay checks before transactional eligibility validation, calls=%d", repository.findCalls)
	}
}

func TestCreateRequestAuthorizesParentBeforeIdempotencyLookup(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "sales-b", Permissions: map[string]bool{"presale.create": true}}
	repo := &createSecurityRepository{old: &PresaleRequest{BaseModel: BaseModel{ID: 91}}}
	opportunities := &accessibleOpportunityReader{err: ErrForbidden}
	service := readyCreateService(repo, opportunities)
	got, err := service.CreateRequest(context.Background(), actor, "guessed-key", validCreateSecurityInput(7))
	if got != nil || !errors.Is(err, ErrForbidden) || repo.findCalls != 0 {
		t.Fatalf("resource=%+v error=%v idempotency lookups=%d", got, err, repo.findCalls)
	}
}

func TestCreateRequestUniqueKeyRaceUsesBoundReplayCheck(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "sales-a", Permissions: map[string]bool{"presale.create": true}}
	input := validCreateSecurityInput(7)
	hash, _, err := createRequestHashes(actor, input)
	if err != nil {
		t.Fatal(err)
	}
	winner := &PresaleRequest{BaseModel: BaseModel{ID: 91, TenantID: actor.TenantID, CreatedBy: actor.UserID}, OpportunityID: 7, ApplicantID: actor.UserID, CreateIdempotencyKey: "race-key", CreateRequestHash: hash}
	repo := &createSecurityRepository{old: winner, missBeforeWinner: 1, transactionErr: errors.New("duplicate key")}
	service := readyCreateService(repo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}})
	got, err := service.CreateRequest(context.Background(), actor, "race-key", input)
	if err != nil || got == nil || got.ID != winner.ID || repo.findCalls != 2 {
		t.Fatalf("resource=%+v error=%v lookups=%d", got, err, repo.findCalls)
	}

	otherActor := Actor{TenantID: actor.TenantID, UserID: "sales-b", Permissions: map[string]bool{"presale.create": true}}
	conflictRepo := &createSecurityRepository{old: winner, missBeforeWinner: 1, transactionErr: errors.New("duplicate key")}
	conflictService := readyCreateService(conflictRepo, &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "OP7"}}})
	got, err = conflictService.CreateRequest(context.Background(), otherActor, "race-key", input)
	if got != nil || !errors.Is(err, ErrIdempotencyConflict) || conflictRepo.findCalls != 2 {
		t.Fatalf("cross-actor race resource=%+v error=%v lookups=%d", got, err, conflictRepo.findCalls)
	}
}

type worklogSecurityRepository struct {
	Repository
	request          *PresaleRequest
	assignments      []Assignment
	old              *Worklog
	findCalls        int
	missBeforeWinner int
	createErr        error
	createCalls      int
}

func (r *worklogSecurityRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *worklogSecurityRepository) FindRequestForUpdate(context.Context, string, uint64) (*PresaleRequest, error) {
	if r.request == nil {
		return nil, ErrNotFound
	}
	value := *r.request
	return &value, nil
}
func (r *worklogSecurityRepository) ListCurrentAssignmentsForUpdate(context.Context, string, uint64) ([]Assignment, error) {
	return append([]Assignment(nil), r.assignments...), nil
}
func (r *worklogSecurityRepository) FindWorklogByKey(context.Context, string, string) (*Worklog, error) {
	r.findCalls++
	if r.findCalls <= r.missBeforeWinner || r.old == nil {
		return nil, ErrNotFound
	}
	value := *r.old
	return &value, nil
}
func (r *worklogSecurityRepository) HasOverlappingWorklog(context.Context, string, uint64, string, time.Time, time.Time) (bool, error) {
	return false, nil
}
func (r *worklogSecurityRepository) NextWorklogNo(context.Context, string, time.Time) (string, error) {
	return "WL202608010001", nil
}
func (r *worklogSecurityRepository) CreateWorklog(_ context.Context, value *Worklog) error {
	r.createCalls++
	if r.createErr != nil {
		return r.createErr
	}
	value.ID = 100
	return nil
}

func validWorklogSecurityInput() AddWorklogInput {
	start := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	return AddWorklogInput{WorkStart: start, WorkEnd: start.Add(time.Hour), RawUnit: "小时", RawValue: "1", WorkSiteAddress: "远程", WorkContent: "技术答疑", Version: 3}
}

func TestAddWorklogReplayIsBoundToCurrentActorAndRequest(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "user-a", PersonID: "person-a", Permissions: map[string]bool{"presale.worklog": true}}
	input := validWorklogSecurityInput()
	hash, _, err := worklogRequestHashes(actor, 7, input)
	if err != nil {
		t.Fatal(err)
	}
	old := &Worklog{BaseModel: BaseModel{ID: 51, TenantID: actor.TenantID, CreatedBy: actor.UserID}, RequestID: 7, PersonID: actor.PersonID, IdempotencyKey: "same-key", RequestHash: hash}

	t.Run("same actor and parent replays after automatic completion", func(t *testing.T) {
		repo := &worklogSecurityRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: actor.TenantID, Version: 4}, Status: StatusCompleted}, assignments: []Assignment{{AssigneeID: actor.PersonID}}, old: old}
		service := NewService(repo, nil, nil, fixedClock{at: input.WorkEnd}, fixedIDs{})
		got, replayErr := service.AddWorklog(context.Background(), actor, 7, "same-key", input)
		if replayErr != nil || got == nil || got.ID != old.ID {
			t.Fatalf("replay=%+v error=%v", got, replayErr)
		}
	})

	for _, test := range []struct {
		name        string
		actor       Actor
		requestID   uint64
		assignments []Assignment
		input       AddWorklogInput
	}{
		{name: "different actor", actor: Actor{TenantID: "tenant-1", UserID: "user-b", PersonID: "person-b", Permissions: map[string]bool{"presale.worklog": true}}, requestID: 7, assignments: []Assignment{{AssigneeID: "person-b"}}, input: input},
		{name: "different request", actor: actor, requestID: 8, assignments: []Assignment{{AssigneeID: actor.PersonID}}, input: input},
		{name: "different payload", actor: actor, requestID: 7, assignments: []Assignment{{AssigneeID: actor.PersonID}}, input: func() AddWorklogInput { changed := input; changed.Remark = "changed"; return changed }()},
	} {
		t.Run(test.name+" conflicts without returning old worklog", func(t *testing.T) {
			repo := &worklogSecurityRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: test.requestID, TenantID: "tenant-1", Version: 3}, Status: StatusExecuting}, assignments: test.assignments, old: old}
			service := NewService(repo, nil, nil, fixedClock{at: input.WorkEnd}, fixedIDs{})
			got, replayErr := service.AddWorklog(context.Background(), test.actor, test.requestID, "same-key", test.input)
			if got != nil || !errors.Is(replayErr, ErrIdempotencyConflict) {
				t.Fatalf("worklog=%+v error=%v", got, replayErr)
			}
		})
	}
}

func TestAddWorklogChecksCurrentAssignmentBeforeKeyLookup(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "user-b", PersonID: "person-b", Permissions: map[string]bool{"presale.worklog": true}}
	repo := &worklogSecurityRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: actor.TenantID, Version: 3}, Status: StatusExecuting}, old: &Worklog{BaseModel: BaseModel{ID: 51}}}
	service := NewService(repo, nil, nil, fixedClock{at: validWorklogSecurityInput().WorkEnd}, fixedIDs{})
	got, err := service.AddWorklog(context.Background(), actor, 7, "guessed-key", validWorklogSecurityInput())
	if got != nil || !errors.Is(err, ErrForbidden) || repo.findCalls != 0 {
		t.Fatalf("worklog=%+v error=%v idempotency lookups=%d", got, err, repo.findCalls)
	}
}

func TestAddWorklogUniqueKeyRaceRepeatsBindingAndAuthorization(t *testing.T) {
	actor := Actor{TenantID: "tenant-1", UserID: "user-a", PersonID: "person-a", Permissions: map[string]bool{"presale.worklog": true}}
	input := validWorklogSecurityInput()
	hash, _, err := worklogRequestHashes(actor, 7, input)
	if err != nil {
		t.Fatal(err)
	}
	winner := &Worklog{BaseModel: BaseModel{ID: 51, TenantID: actor.TenantID, CreatedBy: actor.UserID}, RequestID: 7, PersonID: actor.PersonID, IdempotencyKey: "race-key", RequestHash: hash}
	repo := &worklogSecurityRepository{
		request:     &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: actor.TenantID, Version: 3}, Status: StatusExecuting},
		assignments: []Assignment{{AssigneeID: actor.PersonID, AssigneeNameSnapshot: "Person A"}}, old: winner,
		missBeforeWinner: 1, createErr: errors.New("duplicate key"),
	}
	service := NewService(repo, nil, nil, fixedClock{at: input.WorkEnd}, fixedIDs{})
	got, err := service.AddWorklog(context.Background(), actor, 7, "race-key", input)
	if err != nil || got == nil || got.ID != winner.ID || repo.findCalls != 2 || repo.createCalls != 1 {
		t.Fatalf("worklog=%+v error=%v lookups=%d creates=%d", got, err, repo.findCalls, repo.createCalls)
	}

	conflictRepo := &worklogSecurityRepository{
		request:     &PresaleRequest{BaseModel: BaseModel{ID: 8, TenantID: actor.TenantID, Version: 3}, Status: StatusExecuting},
		assignments: []Assignment{{AssigneeID: actor.PersonID, AssigneeNameSnapshot: "Person A"}}, old: winner,
		missBeforeWinner: 1, createErr: errors.New("duplicate key"),
	}
	conflictService := NewService(conflictRepo, nil, nil, fixedClock{at: input.WorkEnd}, fixedIDs{})
	got, err = conflictService.AddWorklog(context.Background(), actor, 8, "race-key", input)
	if got != nil || !errors.Is(err, ErrIdempotencyConflict) || conflictRepo.findCalls != 2 || conflictRepo.createCalls != 1 {
		t.Fatalf("cross-request race worklog=%+v error=%v lookups=%d creates=%d", got, err, conflictRepo.findCalls, conflictRepo.createCalls)
	}
}
