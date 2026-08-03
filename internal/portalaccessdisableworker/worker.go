package portalaccessdisableworker

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

type operationStore interface {
	claim(context.Context, string, time.Time, time.Duration, int) ([]portalinvite.AccessDisableOperation, error)
	failInvalid(context.Context, portalinvite.AccessDisableOperation, string, time.Time) error
}

type recoveryService interface {
	ResumeClaimed(context.Context, *portalinvite.AccessDisableOperation, string) (*portalinvite.DisableAccessResult, error)
}

type Worker struct {
	store         operationStore
	service       recoveryService
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

func NewWorker(store operationStore, service recoveryService, cfg Config) *Worker {
	return &Worker{
		store: store, service: service, workerID: cfg.WorkerID, pollInterval: cfg.PollInterval,
		leaseDuration: cfg.LeaseDuration, batchSize: cfg.BatchSize, now: func() time.Time { return time.Now().UTC() },
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || w.service == nil || strings.TrimSpace(w.workerID) == "" {
		return errors.New("Portal access disable worker dependencies are incomplete")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal access disable recovery poll failed: %s", safeSummary(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	operations, err := w.store.claim(ctx, w.workerID, w.now(), w.leaseDuration, w.batchSize)
	if err != nil {
		return 0, err
	}
	var joined error
	for i := range operations {
		operation := &operations[i]
		// 稳定请求号贯穿本次恢复调用，便于远端幂等和审计串联；它不包含租户凭据或主体明文。
		opCtx := requestctx.WithID(ctx, "portal-disable-worker:"+operation.OperationNo)
		if !validOperation(operation, w.workerID, w.now()) {
			joined = errors.Join(joined, w.store.failInvalid(opCtx, *operation, w.workerID, w.now()))
			continue
		}
		if _, resumeErr := w.service.ResumeClaimed(opCtx, operation, w.workerID); resumeErr != nil {
			// 服务层已把远端失败持久化为有限重试或死信；轮询层只返回固定错误，避免凭据、主体和上游响应进入日志。
			joined = errors.Join(joined, errors.New("Portal access disable recovery step failed"))
		}
	}
	return len(operations), joined
}

func validOperation(value *portalinvite.AccessDisableOperation, workerID string, now time.Time) bool {
	// 恢复只能从协议允许的中间阶段继续，且必须持有本副本尚未过期的租约；异常行不能直接驱动远端撤权。
	if value == nil || value.ID == 0 || strings.TrimSpace(value.TenantID) == "" || strings.TrimSpace(value.OperationNo) == "" ||
		strings.TrimSpace(value.ActorID) == "" || value.CustomerID == 0 || value.IdentityLinkID == 0 || value.IdentityLinkVersion == 0 ||
		value.ContactID == 0 || strings.TrimSpace(value.PlatformUserID) == "" || strings.TrimSpace(value.PortalAccountID) == "" ||
		strings.TrimSpace(value.Reason) == "" || value.Status != portalinvite.DisableStatusProcessing || value.LockedBy != workerID ||
		value.LockedUntil == nil || !value.LockedUntil.After(now) {
		return false
	}
	return value.Stage == portalinvite.DisableStagePrepared || value.Stage == portalinvite.DisableStageMappingDisabled
}

func safeSummary(err error) string {
	if errors.Is(err, context.Canceled) {
		return context.Canceled.Error()
	}
	if errors.Is(err, errLeaseLost) {
		return errLeaseLost.Error()
	}
	return "Portal access disable recovery processing failed"
}
