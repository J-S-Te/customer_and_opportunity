package presale

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCalculateHours(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		unit  string
		value string
		want  string
		err   bool
	}{
		{name: "hours unchanged", unit: "小时", value: "1.25", want: "1.25"},
		{name: "person day converted", unit: "人天", value: "1", want: "8.00"},
		{name: "quarter person day", unit: "PERSON_DAY", value: "0.25", want: "2.00"},
		{name: "zero rejected", unit: "HOUR", value: "0", err: true},
		{name: "too precise rejected", unit: "HOUR", value: "1.001", err: true},
		{name: "unknown unit rejected", unit: "week", value: "1", err: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := calculateHours(test.unit, test.value, "8.00")
			if test.err && err == nil {
				t.Fatal("expected an error")
			}
			if !test.err && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("calculateHours()=%q, want %q", got, test.want)
			}
		})
	}
}

type completionRepository struct {
	Repository
	have       map[string]bool
	updates    int
	statusLogs int
}

func (r *completionRepository) AssigneeIDsWithValidWorklogs(context.Context, string, uint64, []string) (map[string]bool, error) {
	return r.have, nil
}
func (r *completionRepository) UpdateRequestVersioned(_ context.Context, request *PresaleRequest, version uint64, _ map[string]any) error {
	if request.Version != version {
		return ErrVersionConflict
	}
	r.updates++
	request.Version++
	return nil
}
func (r *completionRepository) CreateStatusLog(context.Context, *StatusLog) error {
	r.statusLogs++
	return nil
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

type fixedIDs struct{}

func (fixedIDs) NewID() string { return "event-1" }

type progressRepository struct {
	Repository
	request            *PresaleRequest
	assignments        []Assignment
	byKey              map[string]*ProgressLog
	transactionCalls   int
	requestLocks       int
	assignmentLocks    int
	createCalls        int
	notificationEvents []ProgressNotificationEvent
	outbox             []OutboxEvent
}

func (r *progressRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	r.transactionCalls++
	return fn(ctx)
}
func (r *progressRepository) FindProgressByKey(_ context.Context, _, key string) (*ProgressLog, error) {
	if value := r.byKey[key]; value != nil {
		copyValue := *value
		return &copyValue, nil
	}
	return nil, ErrNotFound
}
func (r *progressRepository) FindProgressByKeyForUpdate(ctx context.Context, tenant, key string) (*ProgressLog, error) {
	return r.FindProgressByKey(ctx, tenant, key)
}
func (r *progressRepository) FindRequestForUpdate(context.Context, string, uint64) (*PresaleRequest, error) {
	r.requestLocks++
	copyValue := *r.request
	return &copyValue, nil
}
func (r *progressRepository) ListCurrentAssignmentsForUpdate(context.Context, string, uint64) ([]Assignment, error) {
	r.assignmentLocks++
	return append([]Assignment(nil), r.assignments...), nil
}
func (r *progressRepository) CreateProgress(_ context.Context, value *ProgressLog) error {
	r.createCalls++
	value.ID = uint64(r.createCalls)
	copyValue := *value
	r.byKey[value.IdempotencyKey] = &copyValue
	return nil
}
func (r *progressRepository) CreateProgressNotificationEvent(_ context.Context, value *ProgressNotificationEvent) error {
	value.ID = uint64(len(r.notificationEvents) + 1)
	r.notificationEvents = append(r.notificationEvents, *value)
	return nil
}
func (r *progressRepository) CreateOutbox(_ context.Context, value *OutboxEvent) error {
	r.outbox = append(r.outbox, *value)
	return nil
}

type progressRaceRepository struct {
	*progressRepository
	firstReached chan struct{}
	releaseFirst chan struct{}
}

type progressCommitRaceRepository struct {
	*progressRepository
	winner    *ProgressLog
	findCalls int
}

func (r *progressCommitRaceRepository) WithTransaction(context.Context, func(context.Context) error) error {
	return errors.New("duplicate key after another request committed")
}

func (r *progressCommitRaceRepository) FindProgressByKey(_ context.Context, _, key string) (*ProgressLog, error) {
	r.findCalls++
	if r.findCalls == 1 {
		return nil, ErrNotFound
	}
	if r.winner != nil && r.winner.IdempotencyKey == key {
		copyValue := *r.winner
		return &copyValue, nil
	}
	return nil, ErrNotFound
}

func (r *progressRaceRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	r.transactionCalls++
	if r.transactionCalls == 1 {
		close(r.firstReached)
		<-r.releaseFirst
	}
	return fn(ctx)
}

func TestConcurrentProgressRetriesCreateOnlyOneImmutableRecord(t *testing.T) {
	t.Parallel()
	base := &progressRepository{
		request:     &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: "tenant-1", Version: 3}, Status: StatusExecuting},
		assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}, byKey: map[string]*ProgressLog{},
	}
	repo := &progressRaceRepository{progressRepository: base, firstReached: make(chan struct{}), releaseFirst: make(chan struct{})}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	actor := Actor{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1", Permissions: map[string]bool{"presale.progress": true}}
	input := AddProgressInput{Content: "safe progress", Version: 3}
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.AddProgress(context.Background(), actor, 7, "same-key", input)
		firstDone <- err
	}()
	<-repo.firstReached
	secondDone := make(chan error, 1)
	go func() {
		_, err := service.AddProgress(context.Background(), actor, 7, "same-key", input)
		secondDone <- err
	}()
	if err := <-secondDone; err != nil {
		t.Fatalf("second retry failed: %v", err)
	}
	close(repo.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first retry failed: %v", err)
	}
	if repo.createCalls != 1 {
		t.Fatalf("concurrent retries created %d records", repo.createCalls)
	}
}

func TestAddProgressLocksCurrentStateAndReplaysSamePayload(t *testing.T) {
	t.Parallel()
	repo := &progressRepository{
		request:     &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: "tenant-1", Version: 3}, Status: StatusExecuting},
		assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}, byKey: map[string]*ProgressLog{},
	}
	service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)}, fixedIDs{})
	actor := Actor{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1", Permissions: map[string]bool{"presale.progress": true}}
	input := AddProgressInput{Content: " completed POC ", LinkURL: "https://example.invalid/result", Version: 3}
	first, err := service.AddProgress(context.Background(), actor, 7, "progress-key", input)
	if err != nil || first == nil || first.ID == 0 {
		t.Fatalf("first progress=%+v error=%v", first, err)
	}
	second, err := service.AddProgress(context.Background(), actor, 7, "progress-key", input)
	if err != nil || second.ID != first.ID || repo.createCalls != 1 || repo.transactionCalls != 1 || repo.requestLocks != 1 || repo.assignmentLocks != 1 {
		t.Fatalf("replay=%+v error=%v writes=%d tx=%d requestLocks=%d assignmentLocks=%d", second, err, repo.createCalls, repo.transactionCalls, repo.requestLocks, repo.assignmentLocks)
	}
	changed := input
	changed.Content = "different content"
	if _, err = service.AddProgress(context.Background(), actor, 7, "progress-key", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different-payload replay error=%v", err)
	}
}

func TestAddProgressRejectsStaleVersionOrRemovedAssigneeBeforeInsert(t *testing.T) {
	t.Parallel()
	actor := Actor{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1", Permissions: map[string]bool{"presale.progress": true}}
	for _, test := range []struct {
		name        string
		version     uint64
		assignments []Assignment
		want        error
	}{
		{name: "stale version", version: 2, assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}}, want: ErrVersionConflict},
		{name: "removed assignee", version: 3, assignments: nil, want: ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &progressRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: "tenant-1", Version: 3}, Status: StatusExecuting}, assignments: test.assignments, byKey: map[string]*ProgressLog{}}
			service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
			_, err := service.AddProgress(context.Background(), actor, 7, "progress-key", AddProgressInput{Content: "progress", Version: test.version})
			if !errors.Is(err, test.want) || repo.createCalls != 0 {
				t.Fatalf("error=%v writes=%d", err, repo.createCalls)
			}
		})
	}
}

func TestAddProgressRejectsMarkupAndOversizedLink(t *testing.T) {
	t.Parallel()
	repo := &progressRepository{byKey: map[string]*ProgressLog{}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	actor := Actor{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1", Permissions: map[string]bool{"presale.progress": true}}
	for _, input := range []AddProgressInput{
		{Content: "<script>alert(1)</script>", Version: 3},
		{Content: "progress", LinkURL: "https://example.invalid/" + strings.Repeat("x", 1000), Version: 3},
	} {
		if _, err := service.AddProgress(context.Background(), actor, 7, "progress-key", input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input=%+v error=%v, want ErrInvalidInput", input, err)
		}
	}
	if repo.transactionCalls != 0 || repo.createCalls != 0 {
		t.Fatalf("invalid input reached transaction: tx=%d writes=%d", repo.transactionCalls, repo.createCalls)
	}
}

func TestAddProgressResolvesCrossRequestUniqueKeyRace(t *testing.T) {
	t.Parallel()
	actor := Actor{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1", Permissions: map[string]bool{"presale.progress": true}}
	input := AddProgressInput{Content: "progress", Version: 3}
	base := &progressRepository{
		request:     &PresaleRequest{BaseModel: BaseModel{ID: 7, TenantID: "tenant-1", Version: 3}, Status: StatusExecuting},
		assignments: []Assignment{{AssigneeID: "person-1", IsCurrent: true}},
		byKey:       map[string]*ProgressLog{},
	}
	service := NewService(base, nil, nil, fixedClock{}, fixedIDs{})
	// Obtain the canonical hash through a normal successful write, then reuse
	// that immutable record as the concurrently committed winner.
	winner, err := service.AddProgress(context.Background(), actor, 7, "winner-key", input)
	if err != nil {
		t.Fatal(err)
	}
	raceRepo := &progressCommitRaceRepository{progressRepository: &progressRepository{byKey: map[string]*ProgressLog{}}, winner: winner}
	replayService := NewService(raceRepo, nil, nil, fixedClock{}, fixedIDs{})
	replayed, err := replayService.AddProgress(context.Background(), actor, 7, "winner-key", input)
	if err != nil || replayed.ID != winner.ID || raceRepo.findCalls != 2 {
		t.Fatalf("replayed=%+v error=%v", replayed, err)
	}
	changed := input
	changed.Content = "changed"
	conflictRepo := &progressCommitRaceRepository{progressRepository: &progressRepository{byKey: map[string]*ProgressLog{}}, winner: winner}
	conflictService := NewService(conflictRepo, nil, nil, fixedClock{}, fixedIDs{})
	if _, err = conflictService.AddProgress(context.Background(), actor, 8, "winner-key", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-request conflict error=%v", err)
	}
}

func TestCompleteIfReady_AllCurrentAssigneesMustHaveWorklog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		assignments []Assignment
		have        map[string]bool
		want        bool
	}{
		{name: "no current assignment", assignments: nil, have: map[string]bool{}, want: false},
		{name: "single assignee has worklog", assignments: []Assignment{{AssigneeID: "p1", IsCurrent: true}}, have: map[string]bool{"p1": true}, want: true},
		{name: "one of two is missing", assignments: []Assignment{{AssigneeID: "p1", IsCurrent: true}, {AssigneeID: "p2", IsCurrent: true}}, have: map[string]bool{"p1": true}, want: false},
		{name: "all current assignees have worklogs", assignments: []Assignment{{AssigneeID: "p1", IsCurrent: true}, {AssigneeID: "p2", IsCurrent: true}}, have: map[string]bool{"p1": true, "p2": true}, want: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repo := &completionRepository{have: test.have}
			service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)}, fixedIDs{})
			request := &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-1", Version: 3}, Status: StatusExecuting}
			got, err := service.completeIfReady(context.Background(), request, test.assignments, "user-1", "req-1")
			if err != nil {
				t.Fatalf("completeIfReady() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("completeIfReady()=%v, want %v", got, test.want)
			}
			wantWrites := 0
			if test.want {
				wantWrites = 1
			}
			if repo.updates != wantWrites || repo.statusLogs != wantWrites {
				t.Fatalf("writes=(%d,%d), want (%d,%d)", repo.updates, repo.statusLogs, wantWrites, wantWrites)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	now := time.Now().UTC()
	valid := CreateRequestInput{OpportunityID: 1, Venue: VenueOnsite, ServiceAddress: "Shanghai", ContactName: "customer", ContactPhone: "13800000000", Description: "a sufficiently long description", ExpectedStart: now, ExpectedEnd: now.Add(time.Hour), Urgency: UrgencyNormal}
	if err := validateCreate(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	valid.ServiceAddress = ""
	if err := validateCreate(valid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("onsite without address error=%v", err)
	}
	valid.ServiceAddress = "Shanghai"
	valid.Description = " "
	if err := validateCreate(valid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty description error=%v", err)
	}
	valid.Description = strings.Repeat("需求说明 ", 5000)
	if err := validateCreate(valid); err != nil {
		t.Fatalf("long description rejected: %v", err)
	}
}
