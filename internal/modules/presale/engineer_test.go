package presale

import (
	"context"
	"errors"
	"testing"
	"time"
)

type engineerRepoStub struct {
	listTenant string
	query      EngineerListQuery
	job        *EngineerSyncJob
	err        error
}

func (r *engineerRepoStub) ListAssignableEngineers(_ context.Context, tenant string, query EngineerListQuery) (EngineerListPage, error) {
	r.listTenant, r.query = tenant, query
	return EngineerListPage{}, r.err
}
func (r *engineerRepoStub) EnqueueEngineerSync(_ context.Context, tenant, actor, key, hash string, _ time.Time) (*EngineerSyncJob, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.job == nil {
		r.job = &EngineerSyncJob{BaseModel: BaseModel{TenantID: tenant, CreatedAt: time.Now()}, JobNo: "job-1", TriggerType: "MANUAL", RequestedBy: actor, IdempotencyKey: key, RequestHash: hash, Status: "PENDING"}
	}
	return r.job, nil
}

func TestEngineerListUsesActorTenantAndMinimumFilters(t *testing.T) {
	repo := &engineerRepoStub{}
	service := NewEngineerService(repo, fixedClock{}, fixedIDs{})
	_, err := service.List(context.Background(), Actor{TenantID: "tenant-a", Permissions: map[string]bool{"presale.read": true}}, EngineerListQuery{Keyword: " 王 ", Department: " 安全部 ", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if repo.listTenant != "tenant-a" || repo.query.Keyword != "王" || repo.query.Department != "安全部" {
		t.Fatalf("tenant/query=%q %#v", repo.listTenant, repo.query)
	}
}
func TestEngineerListPermissionFailClosed(t *testing.T) {
	service := NewEngineerService(&engineerRepoStub{}, fixedClock{}, fixedIDs{})
	if _, err := service.List(context.Background(), Actor{TenantID: "tenant-a"}, EngineerListQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
}
func TestEngineerSyncRequiresPermissionAndIdempotencyKey(t *testing.T) {
	service := NewEngineerService(&engineerRepoStub{}, fixedClock{}, fixedIDs{})
	actor := Actor{TenantID: "tenant-a", UserID: "u1", Permissions: map[string]bool{"presale.engineer.sync": true}}
	if _, err := service.EnqueueSync(context.Background(), actor, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error=%v", err)
	}
	actor.Permissions = nil
	if _, err := service.EnqueueSync(context.Background(), actor, "key"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
}

type assignmentEngineerLockRepo struct {
	Repository
	engineer Engineer
	locked   bool
}

func (r *assignmentEngineerLockRepo) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *assignmentEngineerLockRepo) FindRequestForUpdate(context.Context, string, uint64) (*PresaleRequest, error) {
	return &PresaleRequest{BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3}, Status: StatusExecuting}, nil
}
func (r *assignmentEngineerLockRepo) ListCurrentAssignmentsForUpdate(context.Context, string, uint64) ([]Assignment, error) {
	return nil, nil
}
func (r *assignmentEngineerLockRepo) FindMutationReplay(context.Context, string, uint64, string, string) (*MutationReplay, error) {
	return nil, ErrNotFound
}
func (r *assignmentEngineerLockRepo) FindEngineersForUpdate(context.Context, string, []string) ([]Engineer, error) {
	r.locked = true
	return []Engineer{r.engineer}, nil
}

func TestReplaceAssignmentsRejectsNewEngineerDeactivatedUnderTransactionLock(t *testing.T) {
	repo := &assignmentEngineerLockRepo{engineer: Engineer{PersonID: "p-1", Role: "implementation_engineer", ValidFlag: false}}
	service := NewService(repo, nil, nil, fixedClock{at: time.Now().UTC()}, fixedIDs{})
	actor := Actor{TenantID: "tenant-a", UserID: "lead", Roles: map[string]bool{"team_lead": true}, Permissions: map[string]bool{"presale.assign": true}}
	_, err := service.ReplaceAssignments(context.Background(), actor, 9, "assignment-key", ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "p-1", Role: "implementation_engineer"}}, ChangeReason: "reassign", Version: 3})
	if !errors.Is(err, ErrInvalidInput) || !repo.locked {
		t.Fatalf("error=%v locked=%v", err, repo.locked)
	}
}
