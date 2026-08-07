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
