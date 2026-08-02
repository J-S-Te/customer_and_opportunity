package portalprojectworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkerSuccessPersistsStableCursorAndReplaysIdempotently(t *testing.T) {
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	state := &syncState{ID: 1, TenantID: "tenant-a", CustomerID: 9, Cursor: "old", LockedBy: "worker-a", Version: 2}
	store := &fakeStore{state: state}
	source := &fakeSource{pages: []sourcePage{{Bundles: []sourceBundle{{ProjectID: "P1", SourceUpdatedAt: now}}, NextCursor: "next", HasMore: false}}}
	worker := workerForTest(store, source, now)
	count, err := worker.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("first run count=%d err=%v", count, err)
	}
	if store.appliedCursors[0] != "next" || source.cursors[0] != "old" {
		t.Fatalf("cursor chain source=%v applied=%v", source.cursors, store.appliedCursors)
	}
	// Replaying the same snapshot is represented by the repository returning no
	// change; the cursor still advances and success is recorded.
	store.state = &syncState{ID: 1, TenantID: "tenant-a", CustomerID: 9, Cursor: "old", LockedBy: "worker-a", Version: 4}
	store.changeCounts = []int{0}
	source.pages = []sourcePage{{Bundles: []sourceBundle{{ProjectID: "P1", SourceUpdatedAt: now}}, NextCursor: "next", HasMore: false}}
	count, err = worker.RunOnce(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("replay count=%d err=%v", count, err)
	}
}

func TestWorkerNeverAppliesCrossCustomerSourceClaim(t *testing.T) {
	// Tenant/customer identifiers are not parsed from source JSON. The only
	// values passed to persistence come from the leased state.
	now := time.Now().UTC()
	state := &syncState{ID: 1, TenantID: "tenant-a", CustomerID: 42, LockedBy: "worker-a", Version: 1}
	store := &fakeStore{state: state}
	source := &fakeSource{pages: []sourcePage{{Bundles: []sourceBundle{{ProjectID: "P1", SourceUpdatedAt: now}}}}}
	worker := workerForTest(store, source, now)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.lastTenant != "tenant-a" || store.lastCustomer != 42 || source.tenant != "tenant-a" || source.customer != 42 {
		t.Fatalf("scope mismatch store=%s/%d source=%s/%d", store.lastTenant, store.lastCustomer, source.tenant, source.customer)
	}
}

func TestWorkerFailureKeepsCursorAndSchedulesRetry(t *testing.T) {
	now := time.Now().UTC()
	state := &syncState{ID: 1, TenantID: "tenant-a", CustomerID: 9, Cursor: "stable", LockedBy: "worker-a", Version: 1}
	store := &fakeStore{state: state}
	source := &fakeSource{err: errors.New("dependency failed")}
	worker := workerForTest(store, source, now)
	_, err := worker.RunOnce(context.Background())
	if err == nil || store.failedCursor != "stable" || !strings.Contains(store.failure, "dependency failed") {
		t.Fatalf("failure err=%v cursor=%q summary=%q", err, store.failedCursor, store.failure)
	}
}

type fakeStore struct {
	state          *syncState
	changeCounts   []int
	appliedCursors []string
	lastTenant     string
	lastCustomer   uint64
	failedCursor   string
	failure        string
}

func (f *fakeStore) seedCustomers(context.Context, string, time.Time) error { return nil }
func (f *fakeStore) claim(context.Context, string, string, time.Time, time.Duration) (*syncState, error) {
	return f.state, nil
}
func (f *fakeStore) applyPage(_ context.Context, state *syncState, _ string, page sourcePage, _ time.Time, _ bool, _, _ time.Duration) (int, error) {
	f.lastTenant, f.lastCustomer = state.TenantID, state.CustomerID
	f.appliedCursors = append(f.appliedCursors, page.NextCursor)
	count := len(page.Bundles)
	if len(f.changeCounts) > 0 {
		count, f.changeCounts = f.changeCounts[0], f.changeCounts[1:]
	}
	state.Cursor, state.Version = page.NextCursor, state.Version+1
	return count, nil
}
func (f *fakeStore) failed(_ context.Context, state *syncState, _ string, _ time.Time, _ time.Duration, summary string) error {
	f.failedCursor, f.failure = state.Cursor, summary
	return nil
}

type fakeSource struct {
	pages    []sourcePage
	err      error
	cursors  []string
	tenant   string
	customer uint64
}

func (f *fakeSource) changed(_ context.Context, tenant string, customer uint64, cursor string) (sourcePage, error) {
	f.tenant, f.customer = tenant, customer
	f.cursors = append(f.cursors, cursor)
	if f.err != nil {
		return sourcePage{}, f.err
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

func workerForTest(store stateStore, source projectSource, now time.Time) *Worker {
	cfg := Config{TenantID: "tenant-a", WorkerID: "worker-a", PollInterval: time.Second, SyncInterval: 5 * time.Minute, LeaseDuration: time.Minute, RetryInterval: time.Minute}
	worker := newWorker(store, source, cfg)
	worker.now = func() time.Time { return now }
	return worker
}
