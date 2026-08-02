package presaleengineersyncworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

type workerStoreStub struct {
	jobs                     []presale.EngineerSyncJob
	renewed, applied, failed int
	snapshot                 SourceSnapshot
	applyErr                 error
	renewErr                 error
}

func (s *workerStoreStub) Schedule(context.Context, time.Time, time.Duration) error { return nil }
func (s *workerStoreStub) Claim(context.Context, string, time.Time, time.Duration, int) ([]presale.EngineerSyncJob, error) {
	return s.jobs, nil
}
func (s *workerStoreStub) Renew(context.Context, presale.EngineerSyncJob, string, time.Time, time.Duration) error {
	s.renewed++
	return s.renewErr
}
func (s *workerStoreStub) Apply(_ context.Context, _ presale.EngineerSyncJob, _ string, snapshot SourceSnapshot, _ time.Time, _ time.Duration) error {
	s.applied++
	s.snapshot = snapshot
	return s.applyErr
}
func (s *workerStoreStub) Fail(context.Context, presale.EngineerSyncJob, string, time.Time, string) error {
	s.failed++
	return nil
}

type sourceStub struct {
	snapshot SourceSnapshot
	err      error
}

func (s sourceStub) Fetch(context.Context, string) (SourceSnapshot, error) { return s.snapshot, s.err }

func TestWorkerRenewsBeforeAndAfterFetchAndAppliesCompleteSnapshot(t *testing.T) {
	now := time.Now().UTC()
	store := &workerStoreStub{jobs: []presale.EngineerSyncJob{{BaseModel: presale.BaseModel{ID: 1, TenantID: "t"}}}}
	snapshot := SourceSnapshot{TenantID: "t", Full: true, Revision: now, Engineers: []SourceEngineer{{PersonID: "p", PersonName: "n", Department: "d", Role: "实施工程师", ValidFlag: true, SyncedAt: now}}}
	worker := NewWorker(store, sourceStub{snapshot: snapshot}, Config{WorkerID: "w", LeaseDuration: time.Minute, BatchSize: 1, SyncInterval: 6 * time.Hour})
	worker.now = func() time.Time { return now }
	count, err := worker.RunOnce(context.Background())
	if err != nil || count != 1 || store.renewed != 2 || store.applied != 1 || store.failed != 0 {
		t.Fatalf("count=%d renew=%d apply=%d fail=%d err=%v", count, store.renewed, store.applied, store.failed, err)
	}
}

func TestWorkerFailureDoesNotApplySnapshot(t *testing.T) {
	store := &workerStoreStub{jobs: []presale.EngineerSyncJob{{BaseModel: presale.BaseModel{ID: 1, TenantID: "t"}}}}
	worker := NewWorker(store, sourceStub{err: errors.New("PMS failed")}, Config{WorkerID: "w", LeaseDuration: time.Minute, SyncInterval: 6 * time.Hour})
	_, err := worker.RunOnce(context.Background())
	if err == nil || store.applied != 0 || store.failed != 1 {
		t.Fatalf("apply=%d fail=%d err=%v", store.applied, store.failed, err)
	}
}

func TestWorkerApplyFailureIsDurablyRetried(t *testing.T) {
	now := time.Now().UTC()
	store := &workerStoreStub{jobs: []presale.EngineerSyncJob{{BaseModel: presale.BaseModel{ID: 1, TenantID: "t"}}}, applyErr: errors.New("transaction failed")}
	snapshot := SourceSnapshot{TenantID: "t", Full: true, Revision: now, Engineers: []SourceEngineer{{PersonID: "p", PersonName: "n", Department: "d", Role: "实施工程师", ValidFlag: true, SyncedAt: now}}}
	worker := NewWorker(store, sourceStub{snapshot: snapshot}, Config{WorkerID: "w", LeaseDuration: time.Minute, SyncInterval: 6 * time.Hour})
	_, err := worker.RunOnce(context.Background())
	if err == nil || store.applied != 1 || store.failed != 1 {
		t.Fatalf("apply=%d fail=%d err=%v", store.applied, store.failed, err)
	}
}

func TestWorkerLeaseLossNeverMutatesFailureState(t *testing.T) {
	store := &workerStoreStub{jobs: []presale.EngineerSyncJob{{BaseModel: presale.BaseModel{ID: 1, TenantID: "t"}}}, renewErr: errLeaseLost}
	worker := NewWorker(store, sourceStub{}, Config{WorkerID: "w", LeaseDuration: time.Minute, SyncInterval: 6 * time.Hour})
	_, err := worker.RunOnce(context.Background())
	if !errors.Is(err, errLeaseLost) || store.applied != 0 || store.failed != 0 {
		t.Fatalf("apply=%d fail=%d err=%v", store.applied, store.failed, err)
	}
}
