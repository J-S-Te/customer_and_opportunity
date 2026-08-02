package portalreportworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

type memoryStore struct {
	events       []report.Outbox
	sentCount    int
	failedCount  int
	lastFailure  string
	failedStatus string
}

func (s *memoryStore) claim(context.Context, string, time.Time, time.Duration, int) ([]report.Outbox, error) {
	events := s.events
	s.events = nil
	return events, nil
}
func (s *memoryStore) sent(context.Context, report.Outbox, string, time.Time) error {
	s.sentCount++
	return nil
}
func (s *memoryStore) failed(_ context.Context, event report.Outbox, _ string, now time.Time, summary string) error {
	s.failedCount++
	s.lastFailure = summary
	s.failedStatus, _ = outboxFailurePlan(now, event.RetryCount+1)
	return nil
}

type projectionStub struct {
	requests []uint64
	err      error
}

func (s *projectionStub) MarkApprovalStarted(_ context.Context, _ string, id uint64, _ string) error {
	s.requests = append(s.requests, id)
	return s.err
}

type projectStub struct {
	downstream string
	err        error
	calls      int
	eventIDs   []string
}

func (s *projectStub) Submit(_ context.Context, event report.Outbox) (string, error) {
	s.calls++
	s.eventIDs = append(s.eventIDs, event.EventID)
	return s.downstream, s.err
}

func testWorker(store workerStore, service projectionService, project projectApprovalPort) *Worker {
	return &Worker{store: store, service: service, project: project, workerID: "worker-1", pollInterval: time.Second, leaseDuration: 30 * time.Second, batchSize: 20, now: func() time.Time { return time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC) }}
}

func TestWorkerSuccessfulDeliveryUpdatesProjectionAndAcknowledges(t *testing.T) {
	store := &memoryStore{events: []report.Outbox{{ID: 1, EventID: "evt-1", TenantID: "tenant-1", EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: 42}}}
	service := &projectionStub{}
	project := &projectStub{downstream: "PR-42"}
	count, err := testWorker(store, service, project).RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if project.calls != 1 || len(service.requests) != 1 || service.requests[0] != 42 || store.sentCount != 1 || store.failedCount != 0 {
		t.Fatalf("unexpected delivery state: project=%+v service=%+v store=%+v", project, service, store)
	}
}

func TestWorkerFailureSchedulesRetryWithoutLeakingSecret(t *testing.T) {
	store := &memoryStore{events: []report.Outbox{{ID: 2, EventID: "evt-2", TenantID: "tenant-1", EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: 43}}}
	project := &projectStub{err: errors.New("oauth token response contains client_secret=hidden")}
	count, err := testWorker(store, &projectionStub{}, project).RunOnce(context.Background())
	if count != 1 || err == nil || store.failedCount != 1 || store.failedStatus != "RETRY_WAIT" || store.lastFailure != "Portal report integration failed" {
		t.Fatalf("retry state count=%d err=%v store=%+v", count, err, store)
	}
}

func TestWorkerSeventhFailureMovesToDeadLetter(t *testing.T) {
	store := &memoryStore{events: []report.Outbox{{ID: 3, EventID: "evt-3", TenantID: "tenant-1", EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: 44, RetryCount: 6}}}
	project := &projectStub{err: errors.New("service unavailable")}
	_, _ = testWorker(store, &projectionStub{}, project).RunOnce(context.Background())
	if store.failedStatus != "DEAD_LETTER" {
		t.Fatalf("status=%q, want DEAD_LETTER", store.failedStatus)
	}
}

func TestExpiredLeaseReplayKeepsStableEventIdentity(t *testing.T) {
	// A worker may see the same event again after a processing lease expires.
	// The downstream port receives the original EventID on every attempt, so
	// the HTTP adapter keeps the same Idempotency-Key.
	event := report.Outbox{ID: 4, EventID: "stable-event-id", TenantID: "tenant-1", EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: 45, Status: "PROCESSING"}
	store := &memoryStore{events: []report.Outbox{event, event}}
	project := &projectStub{downstream: "PR-45"}
	count, err := testWorker(store, &projectionStub{}, project).RunOnce(context.Background())
	if err != nil || count != 2 || len(project.eventIDs) != 2 || project.eventIDs[0] != "stable-event-id" || project.eventIDs[1] != "stable-event-id" || store.sentCount != 2 {
		t.Fatalf("lease replay count=%d err=%v project=%+v store=%+v", count, err, project, store)
	}
}

func TestFailurePlanHasSixFiniteRetries(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for attempt := uint8(1); attempt <= 6; attempt++ {
		status, next := outboxFailurePlan(now, attempt)
		if status != "RETRY_WAIT" || next == nil || !next.After(now) {
			t.Fatalf("attempt %d status=%s next=%v", attempt, status, next)
		}
	}
	status, next := outboxFailurePlan(now, 7)
	if status != "DEAD_LETTER" || next != nil {
		t.Fatalf("attempt 7 status=%s next=%v", status, next)
	}
}
