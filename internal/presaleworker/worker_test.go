package presaleworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

type memoryWorkerStore struct {
	events       []presale.OutboxEvent
	sentCount    int
	failedCount  int
	lastFailure  string
	heartbeats   int
	claimErr     error
	heartbeatErr error
}

func (s *memoryWorkerStore) heartbeat(context.Context, string, time.Time) error {
	s.heartbeats++
	return s.heartbeatErr
}

func TestWorkerDoesNotDispatchWhenClaimAndHeartbeatTransactionFails(t *testing.T) {
	claimErr := errors.New("heartbeat unavailable")
	store := &memoryWorkerStore{claimErr: claimErr, events: []presale.OutboxEvent{{ID: 1, EventType: "PRESALE_APPROVAL_START_REQUESTED"}}}
	approval := &approvalStub{}
	worker := testWorker(store, &memoryWorkerService{}, approval, &pmsStub{})
	count, err := worker.RunOnce(context.Background())
	if count != 0 || !errors.Is(err, claimErr) || approval.starts != 0 || store.sentCount != 0 {
		t.Fatalf("count=%d err=%v approval=%+v store=%+v", count, err, approval, store)
	}
}

func (s *memoryWorkerStore) claim(context.Context, string, time.Time, time.Duration, int) ([]presale.OutboxEvent, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	events := s.events
	s.events = nil
	return events, nil
}
func (s *memoryWorkerStore) sent(context.Context, presale.OutboxEvent, string, time.Time) error {
	s.sentCount++
	return nil
}
func (s *memoryWorkerStore) failed(_ context.Context, _ presale.OutboxEvent, _ string, _ time.Time, summary string) error {
	s.failedCount++
	s.lastFailure = summary
	return nil
}

type memoryWorkerService struct {
	approval presale.ApprovalStartedInput
	sending  int
	success  int
	failure  int
}

func (s *memoryWorkerService) MarkApprovalStarted(_ context.Context, _ string, in presale.ApprovalStartedInput) error {
	s.approval = in
	return nil
}
func (s *memoryWorkerService) MarkDeliverySending(context.Context, string, uint64) error {
	s.sending++
	return nil
}
func (s *memoryWorkerService) MarkDeliverySuccess(context.Context, string, uint64, string) error {
	s.success++
	return nil
}
func (s *memoryWorkerService) MarkDeliveryFailure(context.Context, string, uint64, string, string) error {
	s.failure++
	return nil
}

type approvalStub struct {
	result  presale.ApprovalStartResult
	err     error
	starts  int
	actions int
}

func (s *approvalStub) Start(context.Context, presale.OutboxEvent) (presale.ApprovalStartResult, error) {
	s.starts++
	return s.result, s.err
}
func (s *approvalStub) Act(context.Context, presale.OutboxEvent) error { s.actions++; return s.err }

type pmsStub struct {
	code      string
	err       error
	publishes int
}

func (s *pmsStub) PublishWorklog(context.Context, presale.OutboxEvent) (string, error) {
	s.publishes++
	return s.code, s.err
}

func testWorker(store workerStore, service workerService, approval presale.ApprovalCommandPort, pms presale.PMSPublisher) *Worker {
	return &Worker{store: store, service: service, approval: approval, pms: pms, workerID: "worker-1", pollInterval: time.Second, leaseDuration: 30 * time.Second, batchSize: 20, now: func() time.Time { return time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC) }}
}

func TestWorkerStartsApprovalAndAcknowledgesOutbox(t *testing.T) {
	store := &memoryWorkerStore{events: []presale.OutboxEvent{{ID: 1, EventID: "evt-1", TenantID: "tenant-1", EventType: "PRESALE_APPROVAL_START_REQUESTED", AggregateID: "42"}}}
	service := &memoryWorkerService{}
	approval := &approvalStub{result: presale.ApprovalStartResult{EngineInstanceID: "approval-9", EventSequence: 1}}
	worker := testWorker(store, service, approval, &pmsStub{})
	count, err := worker.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if approval.starts != 1 || service.approval.RequestID != 42 || service.approval.EngineInstanceID != "approval-9" || store.sentCount != 1 || store.failedCount != 0 || store.heartbeats != 1 {
		t.Fatalf("unexpected state: approval=%+v service=%+v store=%+v", approval, service, store)
	}
}

func TestWorkerHeartbeatFailureDoesNotStrandRemainingClaimedEvents(t *testing.T) {
	heartbeatErr := errors.New("heartbeat refresh unavailable")
	store := &memoryWorkerStore{heartbeatErr: heartbeatErr, events: []presale.OutboxEvent{
		{ID: 1, EventID: "evt-1", TenantID: "tenant-1", EventType: "PRESALE_APPROVAL_START_REQUESTED", AggregateID: "41"},
		{ID: 2, EventID: "evt-2", TenantID: "tenant-1", EventType: "PRESALE_APPROVAL_START_REQUESTED", AggregateID: "42"},
	}}
	approval := &approvalStub{result: presale.ApprovalStartResult{EngineInstanceID: "approval-9", EventSequence: 1}}
	worker := testWorker(store, &memoryWorkerService{}, approval, &pmsStub{})
	count, err := worker.RunOnce(context.Background())
	if count != 2 || !errors.Is(err, heartbeatErr) || approval.starts != 2 || store.sentCount != 2 || store.heartbeats != 2 {
		t.Fatalf("count=%d err=%v approval=%+v store=%+v", count, err, approval, store)
	}
}

func TestWorkerPMSFailureUpdatesProjectionAndRequeuesOutbox(t *testing.T) {
	publishErr := errors.New("PMS timeout")
	store := &memoryWorkerStore{events: []presale.OutboxEvent{{ID: 2, EventID: "evt-2", TenantID: "tenant-1", EventType: "PRESALE_WORKLOG_CREATED", AggregateID: "88"}}}
	service := &memoryWorkerService{}
	pms := &pmsStub{err: publishErr}
	worker := testWorker(store, service, &approvalStub{}, pms)
	count, err := worker.RunOnce(context.Background())
	if count != 1 || !errors.Is(err, publishErr) {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if pms.publishes != 1 || service.sending != 1 || service.failure != 1 || service.success != 0 || store.failedCount != 1 || store.sentCount != 0 {
		t.Fatalf("unexpected delivery state: pms=%+v service=%+v store=%+v", pms, service, store)
	}
}

func TestWorkerReclaimedEventCanBeDeliveredIdempotently(t *testing.T) {
	// The same event may be observed again after a lease expires. Its stable
	// EventID is passed unchanged to the HTTP adapter as Idempotency-Key.
	event := presale.OutboxEvent{ID: 3, EventID: "stable-event-id", TenantID: "tenant-1", EventType: "PRESALE_WORKLOG_CREATED", AggregateID: "99", Status: "PROCESSING"}
	store := &memoryWorkerStore{events: []presale.OutboxEvent{event, event}}
	service := &memoryWorkerService{}
	pms := &pmsStub{code: "accepted"}
	worker := testWorker(store, service, &approvalStub{}, pms)
	count, err := worker.RunOnce(context.Background())
	if err != nil || count != 2 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if pms.publishes != 2 || service.sending != 2 || service.success != 2 || store.sentCount != 2 {
		t.Fatalf("recovery path not processed twice: pms=%+v service=%+v store=%+v", pms, service, store)
	}
}
