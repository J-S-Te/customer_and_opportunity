package presale

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

type mutationRepository struct {
	Repository
	request         *PresaleRequest
	assignments     []Assignment
	engineers       []Engineer
	replays         map[string]*MutationReplay
	outboxCount     int
	statusCount     int
	assignmentAdds  int
	requestUpdates  int
	transactionErr  error
	approval        *ApprovalInstance
	approvalLogs    map[string]*ApprovalLog
	commitWinner    *MutationReplay
	findCalls       int
	txCalls         int
	approvalUpdates []map[string]any
}

func (r *mutationRepository) UpdateApprovalInstance(_ context.Context, value *ApprovalInstance, fields map[string]any) error {
	r.approvalUpdates = append(r.approvalUpdates, fields)
	if task, ok := fields["pending_task_id"].(string); ok {
		value.PendingTaskID = task
	}
	if approver, ok := fields["pending_approver"].(string); ok {
		value.PendingApprover = approver
	}
	if action, ok := fields["pending_action"].(string); ok {
		value.PendingAction = action
	}
	if r.approval != nil {
		*r.approval = *value
	}
	return nil
}

func (r *mutationRepository) FindApprovalInstanceForUpdate(context.Context, string, uint64) (*ApprovalInstance, error) {
	if r.approval == nil {
		return nil, ErrNotFound
	}
	value := *r.approval
	return &value, nil
}

func (r *mutationRepository) FindEngineTaskLog(_ context.Context, _ string, taskID string) (*ApprovalLog, error) {
	if value := r.approvalLogs[taskID]; value != nil {
		copyValue := *value
		return &copyValue, nil
	}
	return nil, ErrNotFound
}

func (r *mutationRepository) CreateApprovalLog(_ context.Context, value *ApprovalLog) error {
	if r.approvalLogs == nil {
		r.approvalLogs = make(map[string]*ApprovalLog)
	}
	copyValue := *value
	r.approvalLogs[value.EngineTaskID] = &copyValue
	return nil
}

type fixedApprovalTaskResolver struct{ task ApprovalTask }

func (r fixedApprovalTaskResolver) ResolveCurrentTask(context.Context, ApprovalTaskQuery) (ApprovalTask, error) {
	return r.task, nil
}

func approvalResolver(actor Actor) ApprovalTaskResolver {
	return fixedApprovalTaskResolver{task: ApprovalTask{EngineTaskID: "task-1", EngineInstanceID: "instance-1", Node: 1, ApproverID: actor.UserID}}
}

func (r *mutationRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	r.txCalls++
	if r.transactionErr != nil && r.txCalls == 1 {
		if r.commitWinner != nil {
			r.replays[r.commitWinner.IdempotencyKey] = r.commitWinner
		}
		return r.transactionErr
	}
	return fn(ctx)
}
func (r *mutationRepository) FindRequestForUpdate(context.Context, string, uint64) (*PresaleRequest, error) {
	if r.request == nil {
		return nil, ErrNotFound
	}
	value := *r.request
	return &value, nil
}
func (r *mutationRepository) FindMutationReplay(_ context.Context, _ string, requestID uint64, actorID, key string) (*MutationReplay, error) {
	r.findCalls++
	if value := r.replays[key]; value != nil && value.RequestID == requestID && value.ActorID == actorID {
		copyValue := *value
		return &copyValue, nil
	}
	return nil, ErrNotFound
}
func (r *mutationRepository) CreateMutationReplay(_ context.Context, value *MutationReplay) error {
	if r.replays[value.IdempotencyKey] != nil {
		return errors.New("duplicate key")
	}
	copyValue := *value
	r.replays[value.IdempotencyKey] = &copyValue
	return nil
}
func (r *mutationRepository) CreateOutbox(context.Context, *OutboxEvent) error {
	r.outboxCount++
	return nil
}
func (r *mutationRepository) ListCurrentAssignmentsForUpdate(context.Context, string, uint64) ([]Assignment, error) {
	return append([]Assignment(nil), r.assignments...), nil
}
func (r *mutationRepository) FindEngineersForUpdate(context.Context, string, []string) ([]Engineer, error) {
	return append([]Engineer(nil), r.engineers...), nil
}
func (r *mutationRepository) CreateAssignment(_ context.Context, value *Assignment) error {
	r.assignmentAdds++
	value.ID = uint64(100 + r.assignmentAdds)
	r.assignments = append(r.assignments, *value)
	return nil
}
func (r *mutationRepository) CreateAssignmentEvent(context.Context, *AssignmentEvent) error {
	return nil
}
func (r *mutationRepository) EndAssignment(context.Context, string, uint64, uint64, string, time.Time) error {
	return nil
}
func (r *mutationRepository) UpdateRequestVersioned(_ context.Context, value *PresaleRequest, version uint64, _ map[string]any) error {
	if value.Version != version {
		return ErrVersionConflict
	}
	r.requestUpdates++
	value.Version++
	if r.request != nil {
		r.request.Version = value.Version
	}
	return nil
}
func (r *mutationRepository) CreateStatusLog(context.Context, *StatusLog) error {
	r.statusCount++
	return nil
}
func (r *mutationRepository) AssigneeIDsWithValidWorklogs(context.Context, string, uint64, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func TestApprovalActionIdempotencyBindingAndReplay(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "director-a", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	request := &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1}
	repo := &mutationRepository{request: request, approval: &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING"}, replays: map[string]*MutationReplay{}}
	service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)}, fixedIDs{}).UseApprovalTaskResolver(approvalResolver(actor))
	input := ApprovalActionInput{Action: "pass", Comment: " checked ", Version: 3}
	if err := service.RequestApprovalAction(context.Background(), actor, 9, "approval-key", input); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestApprovalAction(context.Background(), actor, 9, "approval-key", input); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	if repo.outboxCount != 1 || len(repo.replays) != 1 {
		t.Fatalf("outbox=%d replays=%d", repo.outboxCount, len(repo.replays))
	}
	if repo.approval.PendingTaskID != "task-1" || repo.approval.PendingApprover != actor.UserID || repo.approval.PendingAction != "PASS" {
		t.Fatalf("pending approval binding=%#v", repo.approval)
	}
	// The asynchronous callback may advance node 1 to node 2 before the HTTP
	// client receives its response. The original sales director can still
	// replay only their own exact accepted node-1 command.
	repo.request.Status = StatusPendingApproval
	repo.request.CurrentApprovalNode = 2
	repo.request.Version = 4
	if err := service.RequestApprovalAction(context.Background(), actor, 9, "approval-key", input); err != nil {
		t.Fatalf("post-callback replay failed: %v", err)
	}
	changed := input
	changed.Comment = "different"
	if err := service.RequestApprovalAction(context.Background(), actor, 9, "approval-key", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload error=%v", err)
	}
	other := actor
	other.UserID = "director-b"
	if err := service.RequestApprovalAction(context.Background(), other, 9, "approval-key", input); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cross actor error=%v", err)
	}
}

func TestApprovalActionCannotOverwritePendingCallbackBinding(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "director-a", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	repo := &mutationRepository{
		request:  &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval: &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", PendingTaskID: "task-1", PendingApprover: actor.UserID, PendingAction: "PASS"},
		replays:  map[string]*MutationReplay{},
	}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{}).UseApprovalTaskResolver(approvalResolver(actor))
	err := service.RequestApprovalAction(context.Background(), actor, 9, "second-key", ApprovalActionInput{Action: "REJECT", Comment: "reject", Version: 3})
	if !errors.Is(err, ErrInvalidTransition) || repo.outboxCount != 0 || repo.approval.PendingAction != "PASS" {
		t.Fatalf("error=%v outbox=%d pending=%#v", err, repo.outboxCount, repo.approval)
	}
}

func TestApprovalCallbackReplayBindsAllAuthoritativeIdentities(t *testing.T) {
	input := ApprovalCallbackInput{
		RequestID: 9, EngineInstanceID: "instance-1", EngineTaskID: "task-1", EventSequence: 11,
		Node: 1, Result: "PASS", ApproverID: "director-a", ApproverName: "Director", OccurredAt: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC),
	}
	repo := &mutationRepository{
		request:      &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1},
		approval:     &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING", LastEventSeq: 10, PendingTaskID: "task-1", PendingApprover: "director-a", PendingAction: "PASS"},
		approvalLogs: map[string]*ApprovalLog{}, replays: map[string]*MutationReplay{},
	}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	if err := service.HandleApprovalCallback(context.Background(), "tenant-a", input); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleApprovalCallback(context.Background(), "tenant-a", input); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	variants := []ApprovalCallbackInput{input, input, input, input, input}
	variants[0].EngineInstanceID = "instance-other"
	variants[1].EventSequence++
	variants[2].Node = 2
	variants[3].ApproverID = "director-other"
	variants[4].Result, variants[4].Comment = "REJECT", "reject"
	for index, variant := range variants {
		if err := service.HandleApprovalCallback(context.Background(), "tenant-a", variant); !errors.Is(err, ErrInvalidApprovalEvent) {
			t.Fatalf("variant %d error=%v", index, err)
		}
	}
}

func TestApprovalActionAuthorizesCurrentNodeBeforeTenantKeyLookup(t *testing.T) {
	repo := &mutationRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1}, replays: map[string]*MutationReplay{"guessed": {RequestID: 9}}}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	actor := Actor{TenantID: "tenant-a", UserID: "lead", Roles: map[string]bool{"team_lead": true}, Permissions: map[string]bool{"presale.approve": true}}
	err := service.RequestApprovalAction(context.Background(), actor, 9, "guessed", ApprovalActionInput{Action: "PASS", Version: 3})
	if !errors.Is(err, ErrForbidden) || repo.findCalls != 1 {
		t.Fatalf("error=%v key lookups=%d", err, repo.findCalls)
	}
}

func TestApprovalActionRevokedRoleFailsClosedBeforeReplayLookup(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "former-director", Permissions: map[string]bool{"presale.approve": true}}
	repo := &mutationRepository{
		request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 4}, Status: StatusApprovedPendingAssignment},
		replays: map[string]*MutationReplay{"accepted-key": {TenantID: actor.TenantID, RequestID: 9, ActorID: actor.UserID, Operation: "APPROVAL_ACTION", Action: "PASS"}},
	}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	err := service.RequestApprovalAction(context.Background(), actor, 9, "accepted-key", ApprovalActionInput{Action: "PASS", Version: 3})
	if !errors.Is(err, ErrForbidden) || repo.findCalls != 0 {
		t.Fatalf("error=%v key lookups=%d", err, repo.findCalls)
	}
}

func TestReplaceAssignmentsCanonicalReplayAndCancelReplay(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "lead", Roles: map[string]bool{"team_lead": true}, Permissions: map[string]bool{"presale.assign": true}}
	repo := &mutationRepository{
		request:   &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusApprovedPendingAssignment},
		engineers: []Engineer{{PersonID: "p1", PersonName: "P1", Role: "technician", ValidFlag: true}, {PersonID: "p2", PersonName: "P2", Role: "project_manager", ValidFlag: true}},
		replays:   map[string]*MutationReplay{},
	}
	service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)}, fixedIDs{})
	input := ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "p2", Role: "project_manager"}, {PersonID: "p1", Role: "technician"}}, ChangeReason: " initial ", Version: 3}
	if _, err := service.ReplaceAssignments(context.Background(), actor, 9, "assign-key", input); err != nil {
		t.Fatal(err)
	}
	reordered := input
	reordered.Assignees = []AssignmentTarget{input.Assignees[1], input.Assignees[0]}
	if _, err := service.ReplaceAssignments(context.Background(), actor, 9, "assign-key", reordered); err != nil {
		t.Fatalf("canonical replay failed: %v", err)
	}
	if repo.assignmentAdds != 2 || repo.statusCount != 1 {
		t.Fatalf("assignments=%d status logs=%d", repo.assignmentAdds, repo.statusCount)
	}

	cancelActor := Actor{TenantID: actor.TenantID, UserID: "sales-a"}
	cancelRepo := &mutationRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 10, TenantID: actor.TenantID, Version: 2}, ApplicantID: cancelActor.UserID, Status: StatusPendingApproval}, replays: map[string]*MutationReplay{}}
	cancelService := NewService(cancelRepo, nil, nil, fixedClock{at: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)}, fixedIDs{})
	cancelInput := CancelInput{Reason: " duplicate request ", Version: 2}
	if err := cancelService.Cancel(context.Background(), cancelActor, 10, "cancel-key", cancelInput); err != nil {
		t.Fatal(err)
	}
	if err := cancelService.Cancel(context.Background(), cancelActor, 10, "cancel-key", cancelInput); err != nil {
		t.Fatalf("cancel replay failed: %v", err)
	}
	otherActor := cancelActor
	otherActor.UserID = "sales-b"
	if err := cancelService.Cancel(context.Background(), otherActor, 10, "cancel-key", cancelInput); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-actor cancellation replay error=%v", err)
	}
	if cancelRepo.requestUpdates != 1 || cancelRepo.statusCount != 1 {
		t.Fatalf("updates=%d status=%d", cancelRepo.requestUpdates, cancelRepo.statusCount)
	}
}

func TestMutationUniqueKeyRaceReturnsOnlyBoundReplay(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "director-a", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	input := ApprovalActionInput{Action: "PASS", Version: 3}
	hash, err := mutationDigest(actor, 9, "APPROVAL_ACTION", "PASS", struct {
		Comment string `json:"comment"`
		Version uint64 `json:"version"`
	}{"", 3})
	if err != nil {
		t.Fatal(err)
	}
	winner := newMutationReplay(actor, 9, "race-key", "APPROVAL_ACTION", "NODE_1_PASS", hash, 3, time.Now().UTC())
	repo := &mutationRepository{request: &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1}, approval: &ApprovalInstance{EngineInstanceID: "instance-1", CurrentNode: 1, Status: "PENDING"}, replays: map[string]*MutationReplay{}, transactionErr: gorm.ErrDuplicatedKey, commitWinner: winner}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	if err = service.RequestApprovalAction(context.Background(), actor, 9, "race-key", input); err != nil {
		t.Fatalf("bound race replay failed: %v", err)
	}
}

func TestMutationRaceDoesNotTurnUnrelatedDatabaseFailureIntoSuccess(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "director-a", Roles: map[string]bool{"sales_director": true}, Permissions: map[string]bool{"presale.approve": true}}
	request := &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: actor.TenantID, Version: 3}, Status: StatusPendingApproval, CurrentApprovalNode: 1}
	repo := &mutationRepository{request: request, replays: map[string]*MutationReplay{}, transactionErr: errors.New("database unavailable")}
	service := NewService(repo, nil, nil, fixedClock{}, fixedIDs{})
	err := service.RequestApprovalAction(context.Background(), actor, 9, "race-key", ApprovalActionInput{Action: "PASS", Version: 3})
	if err == nil || err.Error() != "database unavailable" || repo.txCalls != 1 {
		t.Fatalf("error=%v transaction calls=%d", err, repo.txCalls)
	}
}

func TestMutationReplayRejectsEveryBindingMismatch(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "actor-a"}
	value := &MutationReplay{TenantID: actor.TenantID, RequestID: 9, ActorID: actor.UserID, Operation: "CANCEL", Action: "CANCEL", RequestHash: "hash-a"}
	tests := []struct {
		name      string
		actor     Actor
		requestID uint64
		operation string
		action    string
		hash      string
	}{
		{name: "actor", actor: Actor{TenantID: actor.TenantID, UserID: "actor-b"}, requestID: 9, operation: "CANCEL", action: "CANCEL", hash: "hash-a"},
		{name: "parent", actor: actor, requestID: 10, operation: "CANCEL", action: "CANCEL", hash: "hash-a"},
		{name: "operation", actor: actor, requestID: 9, operation: "APPROVAL_ACTION", action: "CANCEL", hash: "hash-a"},
		{name: "action", actor: actor, requestID: 9, operation: "CANCEL", action: "PASS", hash: "hash-a"},
		{name: "payload", actor: actor, requestID: 9, operation: "CANCEL", action: "CANCEL", hash: "hash-b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMutationReplay(value, test.actor, test.requestID, test.operation, test.action, test.hash); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
