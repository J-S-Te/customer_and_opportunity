package portalreportworker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

var errLeaseLost = errors.New("Portal report outbox lease was lost")

type projectApprovalPort interface {
	Submit(context.Context, report.Outbox) (string, error)
}

type projectionService interface {
	MarkApprovalStarted(context.Context, string, uint64, string) error
}

type workerStore interface {
	claim(context.Context, string, time.Time, time.Duration, int) ([]report.Outbox, error)
	sent(context.Context, report.Outbox, string, time.Time) error
	failed(context.Context, report.Outbox, string, time.Time, string) error
}

type Worker struct {
	store         workerStore
	service       projectionService
	project       projectApprovalPort
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

func NewWorker(store workerStore, service projectionService, project projectApprovalPort, cfg Config) *Worker {
	return &Worker{
		store: store, service: service, project: project, workerID: cfg.WorkerID,
		pollInterval: cfg.PollInterval, leaseDuration: cfg.LeaseDuration, batchSize: cfg.BatchSize,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || w.service == nil || w.project == nil {
		return errors.New("Portal report worker dependencies are incomplete")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal report worker poll failed: %v", safeLogError(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	events, err := w.store.claim(ctx, w.workerID, w.now(), w.leaseDuration, w.batchSize)
	if err != nil {
		return 0, err
	}
	var joined error
	for i := range events {
		if err = w.dispatch(ctx, events[i]); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return len(events), joined
}

func (w *Worker) dispatch(ctx context.Context, event report.Outbox) error {
	if event.EventType != "PORTAL_REPORT_SUBMITTED" || event.AggregateID == 0 || strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.TenantID) == "" {
		return w.fail(ctx, event, errors.New("invalid Portal report outbox event"))
	}
	downstreamID, err := w.project.Submit(ctx, event)
	if err == nil {
		// 远端提交以 EventID 幂等；本地只有在审批实例 ID 投影成功后才把 Outbox 标为 SENT。
		// 若两步之间失败，重试会再次提交同一事件并收敛到同一审批实例。
		err = w.service.MarkApprovalStarted(ctx, event.TenantID, event.AggregateID, downstreamID)
	}
	if err != nil {
		return w.fail(ctx, event, err)
	}
	return w.store.sent(ctx, event, w.workerID, w.now())
}

func (w *Worker) fail(ctx context.Context, event report.Outbox, cause error) error {
	safe := safeLogError(cause)
	if updateErr := w.store.failed(ctx, event, w.workerID, w.now(), safe); updateErr != nil {
		return errors.Join(errors.New(safe), updateErr)
	}
	return errors.New(safe)
}

func sanitize(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}

func safeLogError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled.Error()
	}
	value := sanitize(err.Error())
	// 对可能包含认证材料的错误整体降级为固定文本，不尝试通过局部遮盖猜测敏感边界。
	if strings.Contains(strings.ToLower(value), "token") || strings.Contains(strings.ToLower(value), "secret") || strings.Contains(strings.ToLower(value), "authorization") {
		return "Portal report integration failed"
	}
	if len(value) == 0 {
		return fmt.Sprintf("Portal report integration failed (%T)", err)
	}
	return value
}
