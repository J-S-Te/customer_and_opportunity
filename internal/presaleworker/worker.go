package presaleworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

var errLeaseLost = errors.New("presale outbox lease was lost")

type Worker struct {
	store         workerStore
	service       workerService
	approval      presale.ApprovalCommandPort
	pms           presale.PMSPublisher
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

type workerStore interface {
	claim(context.Context, string, time.Time, time.Duration, int) ([]presale.OutboxEvent, error)
	heartbeat(context.Context, string, time.Time) error
	sent(context.Context, presale.OutboxEvent, string, time.Time) error
	failed(context.Context, presale.OutboxEvent, string, time.Time, string) error
}

type workerService interface {
	MarkApprovalStarted(context.Context, string, presale.ApprovalStartedInput) error
	MarkDeliverySending(context.Context, string, uint64) error
	MarkDeliverySuccess(context.Context, string, uint64, string) error
	MarkDeliveryFailure(context.Context, string, uint64, string, string) error
}

func NewWorker(store workerStore, service workerService, approval presale.ApprovalCommandPort, pms presale.PMSPublisher, cfg Config) *Worker {
	return &Worker{store: store, service: service, approval: approval, pms: pms, workerID: cfg.WorkerID, pollInterval: cfg.PollInterval, leaseDuration: cfg.LeaseDuration, batchSize: cfg.BatchSize, now: func() time.Time { return time.Now().UTC() }}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || w.service == nil || w.approval == nil || w.pms == nil {
		return errors.New("presale worker dependencies are incomplete")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("presale worker poll failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	now := w.now()
	events, err := w.store.claim(ctx, w.workerID, now, w.leaseDuration, w.batchSize)
	if err != nil {
		return 0, err
	}
	var joined error
	for i := range events {
		if err = w.dispatch(ctx, events[i]); err != nil {
			joined = errors.Join(joined, err)
		}
		// 每次有界外部投递后刷新心跳；心跳失败会暴露给监控，但不阻断本批已领取事件，避免存活记录反向卡住任务。
		if err = w.store.heartbeat(ctx, w.workerID, w.now()); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return len(events), joined
}

func (w *Worker) dispatch(ctx context.Context, event presale.OutboxEvent) error {
	var err error
	switch event.EventType {
	case "PRESALE_APPROVAL_START_REQUESTED":
		err = w.startApproval(ctx, event)
	case "PRESALE_APPROVAL_ACTION_REQUESTED":
		err = w.approval.Act(ctx, event)
	case "PRESALE_WORKLOG_CREATED":
		err = w.publishWorklog(ctx, event)
	default:
		err = fmt.Errorf("unsupported presale event type %q", event.EventType)
	}
	if err != nil {
		if updateErr := w.store.failed(ctx, event, w.workerID, w.now(), err.Error()); updateErr != nil {
			return errors.Join(err, updateErr)
		}
		return err
	}
	return w.store.sent(ctx, event, w.workerID, w.now())
}

func (w *Worker) startApproval(ctx context.Context, event presale.OutboxEvent) error {
	result, err := w.approval.Start(ctx, event)
	if err != nil {
		return err
	}
	requestID, err := aggregateID(event)
	if err != nil {
		return err
	}
	// 远端 Start 以事件 ID 幂等；本地投影失败后重试会取得同一审批实例，再完成状态收敛。
	return w.service.MarkApprovalStarted(ctx, event.TenantID, presale.ApprovalStartedInput{RequestID: requestID, EngineInstanceID: result.EngineInstanceID, EventSequence: result.EventSequence})
}

func (w *Worker) publishWorklog(ctx context.Context, event presale.OutboxEvent) error {
	// 发送前先投影 SENDING，远端结果再落 SUCCESS/FAILURE；Outbox 仅在整个本地状态链完成后标记 SENT。
	worklogID, err := aggregateID(event)
	if err != nil {
		return err
	}
	if err = w.service.MarkDeliverySending(ctx, event.TenantID, worklogID); err != nil {
		return err
	}
	responseCode, publishErr := w.pms.PublishWorklog(ctx, event)
	if publishErr == nil {
		return w.service.MarkDeliverySuccess(ctx, event.TenantID, worklogID, responseCode)
	}
	if projectionErr := w.service.MarkDeliveryFailure(ctx, event.TenantID, worklogID, publishErr.Error(), responseCode); projectionErr != nil {
		return errors.Join(publishErr, projectionErr)
	}
	return publishErr
}

func aggregateID(event presale.OutboxEvent) (uint64, error) {
	id, err := strconv.ParseUint(event.AggregateID, 10, 64)
	if err == nil && id > 0 {
		return id, nil
	}
	var payload struct {
		RequestID uint64 `json:"request_id"`
	}
	if json.Unmarshal(event.Payload, &payload) == nil && payload.RequestID > 0 {
		return payload.RequestID, nil
	}
	return 0, fmt.Errorf("event %s has invalid aggregate id", event.EventID)
}

func sanitize(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}
