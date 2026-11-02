package portalreportworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

// deadLetterRecorder 记录存取层调用顺序，用于断言“先申请终态、后死信落库”。
type deadLetterRecorder struct {
	calls   []string
	err     error
	outbox  report.Outbox
	fields  map[string]any
	tenant  string
	holders string
}

func (r *deadLetterRecorder) MarkDeliveryDeadLetter(_ context.Context, outbox report.Outbox) error {
	r.calls = append(r.calls, "projection")
	r.outbox, r.tenant, r.holders = outbox, outbox.TenantID, outbox.LockedBy
	return r.err
}

func testDeadLetterStore(recorder *deadLetterRecorder, committed *[]string) *outboxStore {
	store := &outboxStore{
		deadLetters: recorder,
		transaction: func(ctx context.Context, fn func(context.Context) error) error {
			// 模拟真实事务：fn 失败即回滚，成功才提交。
			if err := fn(ctx); err != nil {
				*committed = append(*committed, "rollback")
				return err
			}
			*committed = append(*committed, "commit")
			return nil
		},
		updateOutbox: func(_ context.Context, event report.Outbox, workerID string, fields map[string]any) error {
			recorder.calls = append(recorder.calls, "outbox")
			recorder.outbox, recorder.fields = event, fields
			return nil
		},
	}
	return store
}

func deadLetterEvent() report.Outbox {
	return report.Outbox{
		ID: 31, EventID: "outbox-event-31", TenantID: "tenant-a", EventType: report.OutboxEventSubmitted,
		AggregateID: 42, Status: report.OutboxProcessing, LockedBy: "worker-1", RetryCount: 6,
	}
}

func TestDeliveryDeadLetterCommitsRequestTerminalStateBeforeOutbox(t *testing.T) {
	recorder := &deadLetterRecorder{}
	var committed []string
	store := testDeadLetterStore(recorder, &committed)
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	event := deadLetterEvent()
	if err := store.failed(context.Background(), event, "worker-1", now, "downstream unavailable\nwith detail"); err != nil {
		t.Fatalf("failed() err=%v", err)
	}
	if len(recorder.calls) != 2 || recorder.calls[0] != "projection" || recorder.calls[1] != "outbox" {
		t.Fatalf("dead letter order=%v, want projection before outbox", recorder.calls)
	}
	if len(committed) != 1 || committed[0] != "commit" {
		t.Fatalf("transaction=%v", committed)
	}
	if recorder.tenant != event.TenantID || recorder.holders != event.LockedBy || recorder.outbox.EventID != event.EventID || recorder.outbox.AggregateID != event.AggregateID {
		t.Fatalf("projection received %+v", recorder.outbox)
	}
	deadLetterNext, typed := recorder.fields["next_retry_at"].(*time.Time)
	if recorder.fields["status"] != report.OutboxDeadLetter || recorder.fields["retry_count"] != uint8(7) || !typed || deadLetterNext != nil {
		t.Fatalf("dead letter fields=%+v", recorder.fields)
	}
	if recorder.fields["last_error_summary"] != "downstream unavailable with detail" || recorder.fields["locked_by"] != "" {
		t.Fatalf("dead letter summary/lock=%+v", recorder.fields)
	}
}

func TestDeliveryDeadLetterRollsBackWhenRequestCannotReachTerminalState(t *testing.T) {
	// 申请已被其他路径推进或租约丢失：整笔回滚，不写没有终态的死信。
	recorder := &deadLetterRecorder{err: errors.New("report delivery lease was lost")}
	var committed []string
	store := testDeadLetterStore(recorder, &committed)
	event := deadLetterEvent()
	if err := store.failed(context.Background(), event, "worker-1", time.Now().UTC(), "downstream unavailable"); err == nil {
		t.Fatal("failed() unexpectedly succeeded")
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "projection" {
		t.Fatalf("calls=%v, dead letter must not be written after projection failure", recorder.calls)
	}
	if len(committed) != 1 || committed[0] != "rollback" {
		t.Fatalf("transaction=%v", committed)
	}
}

func TestDeliveryFailureBeforeDeadLetterOnlySchedulesRetry(t *testing.T) {
	recorder := &deadLetterRecorder{}
	var committed []string
	store := testDeadLetterStore(recorder, &committed)
	event := deadLetterEvent()
	event.RetryCount = 0
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	if err := store.failed(context.Background(), event, "worker-1", now, "timeout"); err != nil {
		t.Fatalf("failed() err=%v", err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "outbox" {
		t.Fatalf("retry calls=%v, request terminal state must stay unchanged", recorder.calls)
	}
	if recorder.fields["status"] != report.OutboxRetryWait || recorder.fields["retry_count"] != uint8(1) {
		t.Fatalf("retry fields=%+v", recorder.fields)
	}
	next, ok := recorder.fields["next_retry_at"].(*time.Time)
	if !ok || next == nil || !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("next_retry_at=%v", recorder.fields["next_retry_at"])
	}
}

func TestDeliveryDeadLetterWithoutRequestIdentitySkipsProjection(t *testing.T) {
	// 结构无效的 Outbox 没有可绑定的申请，只落死信供运维清理，避免租约过期后无限重投。
	recorder := &deadLetterRecorder{}
	var committed []string
	store := testDeadLetterStore(recorder, &committed)
	event := deadLetterEvent()
	event.AggregateID = 0
	if err := store.failed(context.Background(), event, "worker-1", time.Now().UTC(), "invalid event"); err != nil {
		t.Fatalf("failed() err=%v", err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "outbox" {
		t.Fatalf("calls=%v", recorder.calls)
	}
	if recorder.fields["status"] != report.OutboxDeadLetter {
		t.Fatalf("fields=%+v", recorder.fields)
	}
}

func TestDeliveryDeadLetterRequiresConfiguredProjection(t *testing.T) {
	var committed []string
	store := &outboxStore{
		transaction: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
	}
	if err := store.failed(context.Background(), deadLetterEvent(), "worker-1", time.Now().UTC(), "downstream unavailable"); err == nil {
		t.Fatal("dead letter without projection must fail closed")
	}
	if len(committed) != 0 {
		t.Fatalf("committed=%v", committed)
	}
}

func TestDeliveryDeadLetterLeaseFailureFromStoreIsReported(t *testing.T) {
	// Outbox 行更新（租约已失效）失败时同样把错误返回给 Worker，由调用方决定重试。
	recorder := &deadLetterRecorder{}
	var committed []string
	store := testDeadLetterStore(recorder, &committed)
	store.updateOutbox = func(context.Context, report.Outbox, string, map[string]any) error { return errLeaseLost }
	if err := store.failed(context.Background(), deadLetterEvent(), "worker-1", time.Now().UTC(), "downstream unavailable"); !errors.Is(err, errLeaseLost) {
		t.Fatalf("err=%v", err)
	}
}
