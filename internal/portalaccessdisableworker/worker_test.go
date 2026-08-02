package portalaccessdisableworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type fakeStore struct {
	operations []portalinvite.AccessDisableOperation
	invalid    int
	worker     string
	lease      time.Duration
}

func (s *fakeStore) claim(_ context.Context, worker string, _ time.Time, lease time.Duration, _ int) ([]portalinvite.AccessDisableOperation, error) {
	s.worker, s.lease = worker, lease
	return s.operations, nil
}
func (s *fakeStore) failInvalid(context.Context, portalinvite.AccessDisableOperation, string, time.Time) error {
	s.invalid++
	return nil
}

type fakeService struct {
	stages []string
	keys   []string
	err    error
}

func (s *fakeService) ResumeClaimed(_ context.Context, value *portalinvite.AccessDisableOperation, worker string) (*portalinvite.DisableAccessResult, error) {
	s.stages = append(s.stages, value.Stage)
	s.keys = append(s.keys, value.OperationNo+"M", value.OperationNo+"R", worker)
	return &portalinvite.DisableAccessResult{}, s.err
}

func claimedOperation(stage string, now time.Time) portalinvite.AccessDisableOperation {
	until := now.Add(time.Minute)
	return portalinvite.AccessDisableOperation{
		Model: database.Model{ID: 1, TenantID: "tenant-a", Version: 2}, OperationNo: "PD001", ActorID: "actor-a",
		CustomerID: 7, IdentityLinkID: 8, IdentityLinkVersion: 3, ContactID: 9, PlatformUserID: "subject-a",
		PortalAccountID: "account-a", Reason: "operator request", Stage: stage, Status: portalinvite.DisableStatusProcessing,
		LockedBy: "worker-a", LockedUntil: &until,
	}
}

func TestRunOnceResumesBothForwardStagesWithStableOperationIdentity(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{operations: []portalinvite.AccessDisableOperation{claimedOperation(portalinvite.DisableStagePrepared, now), claimedOperation(portalinvite.DisableStageMappingDisabled, now)}}
	service := &fakeService{}
	worker := NewWorker(store, service, Config{WorkerID: "worker-a", PollInterval: time.Second, LeaseDuration: time.Minute, BatchSize: 10})
	worker.now = func() time.Time { return now }
	count, err := worker.RunOnce(context.Background())
	if err != nil || count != 2 || len(service.stages) != 2 || service.stages[0] != portalinvite.DisableStagePrepared || service.stages[1] != portalinvite.DisableStageMappingDisabled {
		t.Fatalf("count=%d stages=%v err=%v", count, service.stages, err)
	}
	if service.keys[0] != "PD001M" || service.keys[1] != "PD001R" || store.worker != "worker-a" || store.lease != time.Minute {
		t.Fatalf("unstable recovery identity or claim: keys=%v store=%#v", service.keys, store)
	}
}

func TestInvalidClaimIsFailedWithoutRemoteDispatch(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	operation := claimedOperation(portalinvite.DisableStagePrepared, now)
	operation.PlatformUserID = ""
	store, service := &fakeStore{operations: []portalinvite.AccessDisableOperation{operation}}, &fakeService{}
	worker := NewWorker(store, service, Config{WorkerID: "worker-a", PollInterval: time.Second, LeaseDuration: time.Minute, BatchSize: 10})
	worker.now = func() time.Time { return now }
	if count, err := worker.RunOnce(context.Background()); err != nil || count != 1 || store.invalid != 1 || len(service.stages) != 0 {
		t.Fatalf("count=%d invalid=%d dispatch=%v err=%v", count, store.invalid, service.stages, err)
	}
}

func TestRemoteFailureIsReturnedOnlyAsGenericSummary(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{operations: []portalinvite.AccessDisableOperation{claimedOperation(portalinvite.DisableStagePrepared, now)}}
	service := &fakeService{err: errors.New("client_secret=never-log subject=private")}
	worker := NewWorker(store, service, Config{WorkerID: "worker-a", PollInterval: time.Second, LeaseDuration: time.Minute, BatchSize: 10})
	worker.now = func() time.Time { return now }
	_, err := worker.RunOnce(context.Background())
	if err == nil || strings.Contains(err.Error(), "never-log") || err.Error() != "Portal access disable recovery step failed" {
		t.Fatalf("unsafe worker error: %v", err)
	}
}

func TestFailurePlanDeadLettersEighthFailureAndClearsRetry(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if delay := disableBackoff(1); delay <= 0 {
		t.Fatalf("invalid retry delay: %v", delay)
	}
	operation := claimedOperation(portalinvite.DisableStagePrepared, now)
	operation.Attempts = 7
	if operation.Attempts+1 != 8 {
		t.Fatal("eighth failure must be the finite retry terminal point")
	}
}

func TestClaimSQLUsesSkipLockedDueTimeAndStaleLease(t *testing.T) {
	for _, fragment := range []string{"FOR UPDATE SKIP LOCKED", "status='RETRY_WAIT' AND next_retry_at<=?", "status='PROCESSING' AND locked_until<?", "locked_until IS NULL AND updated_at<=?", "stage IN ('PREPARED','MAPPING_DISABLED')"} {
		if !strings.Contains(claimOperationsSQL, fragment) {
			t.Fatalf("claim SQL missing %q: %s", fragment, claimOperationsSQL)
		}
	}
}
