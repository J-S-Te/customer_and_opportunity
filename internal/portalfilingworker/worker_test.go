package portalfilingworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/filing"
)

type storeStub struct {
	events       []filing.SubmissionOutbox
	bundle       SubmissionBundle
	loadErr      error
	completed    int
	failed       int
	lastAttempt  uint32
	lastNext     *time.Time
	lastWorkerID string
	lastReceipt  Receipt
	claimSizes   []int
}

func (s *storeStub) Activate(context.Context, string, time.Time, int) (int64, error) { return 0, nil }

func (s *storeStub) Claim(_ context.Context, workerID string, now time.Time, lease time.Duration, size int) ([]filing.SubmissionOutbox, error) {
	s.claimSizes = append(s.claimSizes, size)
	if len(s.events) == 0 {
		return nil, nil
	}
	count := size
	if count > len(s.events) {
		count = len(s.events)
	}
	events := append([]filing.SubmissionOutbox(nil), s.events[:count]...)
	s.events = s.events[count:]
	lockedUntil := now.Add(lease)
	for i := range events {
		events[i].Status = "PROCESSING"
		events[i].LockedBy = workerID
		events[i].LockedUntil = &lockedUntil
	}
	return events, nil
}
func (s *storeStub) LoadBundle(context.Context, Protector, filing.SubmissionOutbox) (SubmissionBundle, error) {
	return s.bundle, s.loadErr
}
func (s *storeStub) Complete(_ context.Context, workerID string, _ filing.SubmissionOutbox, receipt Receipt, _ time.Time) error {
	s.completed++
	s.lastWorkerID = workerID
	s.lastReceipt = receipt
	return nil
}
func (s *storeStub) Fail(_ context.Context, workerID string, _ filing.SubmissionOutbox, _ string, attempt uint32, next *time.Time, _ time.Time) error {
	s.failed++
	s.lastWorkerID, s.lastAttempt, s.lastNext = workerID, attempt, next
	return nil
}

type protectorStub struct{}

func (protectorStub) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	return append([]byte("cipher:"), value...), nil
}
func (protectorStub) Decrypt(context.Context, []byte) ([]byte, error) { return nil, nil }

type providerStub struct {
	available bool
	receipt   Receipt
	err       error
	calls     int
}

func (p *providerStub) Available() bool { return p.available }
func (p *providerStub) Submit(_ context.Context, value SubmissionBundle) (Receipt, error) {
	p.calls++
	if value.EventID == "" {
		return Receipt{}, errors.New("missing event id")
	}
	return p.receipt, p.err
}

func TestWorkerRequiresAvailableFormalProvider(t *testing.T) {
	if _, err := NewWorkerWithStore(&storeStub{}, protectorStub{}, &providerStub{}, "worker", time.Second, 30*time.Second, 10); err == nil {
		t.Fatal("unavailable provider was accepted")
	}
}

func TestWorkerCompletesOnlyAfterValidProviderReceipt(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	event := filing.SubmissionOutbox{ID: 1, EventID: "event-1", RetryCount: 0, CreatedAt: now.Add(-time.Minute)}
	store := &storeStub{events: []filing.SubmissionOutbox{event}, bundle: SubmissionBundle{EventID: "event-1"}}
	provider := &providerStub{available: true, receipt: Receipt{ID: "receipt-1", Authority: "authority-1", ReceivedAt: now, Evidence: []byte("signed receipt")}}
	worker, err := NewWorkerWithStore(store, protectorStub{}, provider, "worker-1", time.Second, 30*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return now }
	if count, runErr := worker.RunOnce(context.Background()); runErr != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d error=%v", count, runErr)
	}
	if provider.calls != 1 || store.completed != 1 || store.failed != 0 || store.lastWorkerID != "worker-1" {
		t.Fatalf("provider calls=%d completed=%d failed=%d worker=%q", provider.calls, store.completed, store.failed, store.lastWorkerID)
	}
	for _, size := range store.claimSizes {
		if size != 1 {
			t.Fatalf("worker claimed %d rows before serial dispatch", size)
		}
	}
	digest := sha256.Sum256([]byte("signed receipt"))
	if len(store.lastReceipt.Evidence) != 0 || string(store.lastReceipt.EvidenceCipher) != "cipher:signed receipt" || store.lastReceipt.EvidenceSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("Store received unprotected or incorrectly bound receipt: %#v", store.lastReceipt)
	}
}

func TestWorkerRejectsUnverifiedReceiptAndDeadLettersSeventhAttempt(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := &storeStub{events: []filing.SubmissionOutbox{{ID: 1, EventID: "event-1", RetryCount: 6, CreatedAt: now.Add(-time.Minute)}}, bundle: SubmissionBundle{EventID: "event-1"}}
	provider := &providerStub{available: true, receipt: Receipt{ID: "receipt-1", Authority: "authority-1", ReceivedAt: now}}
	worker, err := NewWorkerWithStore(store, protectorStub{}, provider, "worker-1", time.Second, 30*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return now }
	if count, runErr := worker.RunOnce(context.Background()); runErr == nil || count != 1 {
		t.Fatalf("RunOnce() count=%d error=%v", count, runErr)
	}
	if store.completed != 0 || store.failed != 1 || store.lastAttempt != 7 || store.lastNext != nil {
		t.Fatalf("completed=%d failed=%d attempt=%d next=%v", store.completed, store.failed, store.lastAttempt, store.lastNext)
	}
}

func TestWorkerRejectsReceiptPredatingSubmission(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := &storeStub{events: []filing.SubmissionOutbox{{ID: 1, EventID: "event-1", CreatedAt: now}}, bundle: SubmissionBundle{EventID: "event-1"}}
	provider := &providerStub{available: true, receipt: Receipt{ID: "receipt-1", Authority: "authority-1", ReceivedAt: now.Add(-6 * time.Minute), Evidence: []byte("signed receipt")}}
	worker, err := NewWorkerWithStore(store, protectorStub{}, provider, "worker-1", time.Second, 30*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return now }
	if _, runErr := worker.RunOnce(context.Background()); runErr == nil {
		t.Fatal("receipt predating the immutable submission was accepted")
	}
	if store.completed != 0 || store.failed != 1 {
		t.Fatalf("completed=%d failed=%d", store.completed, store.failed)
	}
}
