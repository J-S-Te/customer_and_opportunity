package portalinvitecompensationworker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type fakeStore struct {
	tasks            []portalinvite.CompensationTask
	completedRole    int
	completedMapping int
	failedTasks      []failure
	completedAccount string
	claimWorker      string
	claimLease       time.Duration
	claimBatch       int
	queue            queueStats
	statsErr         error
}

func (s *fakeStore) claim(_ context.Context, worker string, _ time.Time, lease time.Duration, batch int) ([]portalinvite.CompensationTask, error) {
	s.claimWorker, s.claimLease, s.claimBatch = worker, lease, batch
	return s.tasks, nil
}
func (s *fakeStore) completeRole(context.Context, portalinvite.CompensationTask, string, time.Time) error {
	s.completedRole++
	return nil
}
func (s *fakeStore) completeMapping(_ context.Context, _ portalinvite.CompensationTask, _ string, mapping portalinvite.PortalMapping, _ time.Time) error {
	s.completedMapping++
	s.completedAccount = mapping.PortalAccountID
	return nil
}
func (s *fakeStore) failed(_ context.Context, _ portalinvite.CompensationTask, _ string, _ time.Time, value failure) error {
	s.failedTasks = append(s.failedTasks, value)
	return nil
}
func (s *fakeStore) stats(context.Context) (queueStats, error) { return s.queue, s.statsErr }

type fakeRoles struct{ err error }

func (f fakeRoles) AssignPortalRole(context.Context, portalinvite.CompensationTask) error {
	return f.err
}

type fakeIdentityReconciler struct {
	calls   int
	metrics reconciliationMetrics
	err     error
}

func (r *fakeIdentityReconciler) RunOnce(context.Context) (reconciliationMetrics, error) {
	r.calls++
	return r.metrics, r.err
}

func TestReconciliationRunsOnStartupAndAtInterval(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	reconciler := &fakeIdentityReconciler{}
	worker := NewWorker(&fakeStore{}, fakeRoles{}, fakeMappings{}, testConfig()).withReconciler(reconciler, time.Minute)
	worker.now = func() time.Time { return now }
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reconciler.calls != 1 {
		t.Fatalf("calls before interval=%d", reconciler.calls)
	}
	now = now.Add(time.Minute)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reconciler.calls != 2 {
		t.Fatalf("calls after interval=%d", reconciler.calls)
	}

	// A restarted process has no in-memory deadline and reconciles immediately;
	// durable run/finding rows make this safe and observable.
	restarted := NewWorker(&fakeStore{}, fakeRoles{}, fakeMappings{}, testConfig()).withReconciler(reconciler, time.Minute)
	restarted.now = worker.now
	if _, err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reconciler.calls != 3 {
		t.Fatalf("restart did not trigger reconciliation: calls=%d", reconciler.calls)
	}
}

func TestReconciliationFailureDoesNotLoseCompletedCompensation(t *testing.T) {
	store := &fakeStore{tasks: []portalinvite.CompensationTask{validTask(portalinvite.CompensationRole)}}
	reconciler := &fakeIdentityReconciler{err: errors.New("snapshot unavailable")}
	worker := NewWorker(store, fakeRoles{}, fakeMappings{}, testConfig()).withReconciler(reconciler, time.Minute)
	count, err := worker.RunOnce(context.Background())
	if count != 1 || err == nil || store.completedRole != 1 || reconciler.calls != 1 {
		t.Fatalf("count=%d completed=%d reconciliation_calls=%d err=%v", count, store.completedRole, reconciler.calls, err)
	}
}

func TestRunOnceReportsQueueStatsFailureWithoutLosingCompletedWork(t *testing.T) {
	store := &fakeStore{tasks: []portalinvite.CompensationTask{validTask(portalinvite.CompensationRole)}, statsErr: errors.New("stats unavailable")}
	worker := NewWorker(store, fakeRoles{}, fakeMappings{}, testConfig())
	count, err := worker.RunOnce(context.Background())
	if count != 1 || err == nil || store.completedRole != 1 {
		t.Fatalf("count=%d completed=%d err=%v", count, store.completedRole, err)
	}
}

type fakeMappings struct {
	result portalinvite.PortalMapping
	err    error
}

func (f fakeMappings) ProvisionMapping(context.Context, portalinvite.CompensationTask) (portalinvite.PortalMapping, error) {
	return f.result, f.err
}

func validTask(taskType string) portalinvite.CompensationTask {
	return portalinvite.CompensationTask{
		TaskNo: "PC001", TaskType: taskType, CustomerID: 7, ContactID: 9,
		PlatformUserID: "subject-1", AccountNo: "account-1", Status: portalinvite.CompensationProcessing,
		Model: database.Model{TenantID: "tenant-a"},
	}
}

func testConfig() Config {
	return Config{WorkerID: "worker-a", PollInterval: time.Second, LeaseDuration: time.Minute, BatchSize: 10}
}

func TestRunOnceDispatchesRoleAndMappingByType(t *testing.T) {
	store := &fakeStore{tasks: []portalinvite.CompensationTask{validTask(portalinvite.CompensationRole), validTask(portalinvite.CompensationMapping)}}
	worker := NewWorker(store, fakeRoles{}, fakeMappings{result: portalinvite.PortalMapping{PortalAccountID: "PA42"}}, testConfig())
	worker.now = func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) }
	count, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || store.completedRole != 1 || store.completedMapping != 1 || store.completedAccount != "PA42" {
		t.Fatalf("count=%d role=%d mapping=%d account=%q", count, store.completedRole, store.completedMapping, store.completedAccount)
	}
	if store.claimWorker != "worker-a" || store.claimLease != time.Minute || store.claimBatch != 10 {
		t.Fatalf("unexpected claim arguments: %#v", store)
	}
}

func TestUnavailableRoleFailsClosedWithoutLeakingCause(t *testing.T) {
	store := &fakeStore{tasks: []portalinvite.CompensationTask{validTask(portalinvite.CompensationRole)}}
	worker := NewWorker(store, fakeRoles{err: errors.New("secret=do-not-store user=alice@example.com")}, fakeMappings{}, testConfig())
	_, err := worker.RunOnce(context.Background())
	if err == nil || len(store.failedTasks) != 1 {
		t.Fatalf("error=%v failures=%#v", err, store.failedTasks)
	}
	got := store.failedTasks[0]
	if got.code != "PLATFORM_ROLE_ASSIGN_UNAVAILABLE" || got.summary != "platform role assignment is unavailable" {
		t.Fatalf("unsafe persisted failure: %#v", got)
	}
}

func TestPortalMappingFaultIsPersistedForBoundedRetryWithoutLeakingCause(t *testing.T) {
	store := &fakeStore{tasks: []portalinvite.CompensationTask{validTask(portalinvite.CompensationMapping)}}
	worker := NewWorker(store, fakeRoles{}, fakeMappings{err: errors.New("upstream response contained customer-secret")}, testConfig())
	_, err := worker.RunOnce(context.Background())
	if err == nil || len(store.failedTasks) != 1 || store.completedMapping != 0 {
		t.Fatalf("error=%v failures=%#v completed=%d", err, store.failedTasks, store.completedMapping)
	}
	if got := store.failedTasks[0]; got.code != "PORTAL_MAPPING_RETRY_FAILED" || got.summary != "Portal mapping retry failed" || strings.Contains(got.summary, "customer-secret") {
		t.Fatalf("unsafe mapping failure=%#v", got)
	}
}

func TestFailurePlanAllowsSixRetriesAndDeadLettersSeventhFailure(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for attempt := uint8(1); attempt <= 6; attempt++ {
		status, next := failurePlan(now, attempt)
		if status != portalinvite.CompensationRetryWait || next == nil || !next.After(now) {
			t.Fatalf("attempt %d: status=%s next=%v", attempt, status, next)
		}
	}
	status, next := failurePlan(now, 7)
	if status != portalinvite.CompensationDeadLetter || next != nil {
		t.Fatalf("seventh failure: status=%s next=%v", status, next)
	}
}

func TestInvalidLegacyTaskIsObservableAndNotDispatched(t *testing.T) {
	task := validTask(portalinvite.CompensationMapping)
	task.AccountNo = ""
	store := &fakeStore{tasks: []portalinvite.CompensationTask{task}}
	worker := NewWorker(store, fakeRoles{}, fakeMappings{result: portalinvite.PortalMapping{PortalAccountID: "PA42"}}, testConfig())
	_, err := worker.RunOnce(context.Background())
	if err == nil || len(store.failedTasks) != 1 || store.failedTasks[0].code != "INVALID_TASK" || store.completedMapping != 0 {
		t.Fatalf("error=%v store=%#v", err, store)
	}
}

func TestSanitizeSummaryRemovesControlsAndLimitsLength(t *testing.T) {
	value := sanitizeSummary("  first\nsecond\t" + strings.Repeat("x", 600) + "  ")
	if len([]rune(value)) > 500 || value[:12] != "first second" {
		t.Fatalf("summary not sanitized: len=%d value=%q", len([]rune(value)), value[:20])
	}
}

func TestClaimSQLUsesSkipLockedAndRecoversExpiredLease(t *testing.T) {
	for _, required := range []string{"FOR UPDATE SKIP LOCKED", "status='PROCESSING' AND locked_until<?", "status IN ('PENDING','RETRY_WAIT')"} {
		if !strings.Contains(claimTasksSQL, required) {
			t.Fatalf("claim SQL missing %q: %s", required, claimTasksSQL)
		}
	}
}

func TestMappingCompletionSourceContainsTerminalDisableFence(t *testing.T) {
	source, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`if link.Status == "DISABLED"`, `return errLeaseLost`} {
		if !strings.Contains(string(source), required) {
			t.Fatalf("mapping completion is missing terminal disable fence %q", required)
		}
	}
}
