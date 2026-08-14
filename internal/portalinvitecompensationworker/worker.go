package portalinvitecompensationworker

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
)

var errLeaseLost = errors.New("Portal invite compensation lease was lost")

type taskStore interface {
	claim(context.Context, string, time.Time, time.Duration, int) ([]portalinvite.CompensationTask, error)
	completeRole(context.Context, portalinvite.CompensationTask, string, time.Time) error
	completeMapping(context.Context, portalinvite.CompensationTask, string, portalinvite.PortalMapping, time.Time) error
	completeBinding(context.Context, portalinvite.CompensationTask, string, time.Time) error
	failed(context.Context, portalinvite.CompensationTask, string, time.Time, failure) error
	stats(context.Context) (queueStats, error)
}

type roleAssigner interface {
	AssignPortalRole(context.Context, portalinvite.CompensationTask) error
}

type mappingProvisioner interface {
	ProvisionMapping(context.Context, portalinvite.CompensationTask) (portalinvite.PortalMapping, error)
}

// bindingRepair 按补偿任务修复平台客户绑定（BIND / DISABLE_BIND 共用同一任务表）。
type bindingRepair interface {
	RepairBinding(context.Context, portalinvite.CompensationTask) error
}

type identityReconciler interface {
	RunOnce(context.Context) (reconciliationMetrics, error)
}

type Worker struct {
	store          taskStore
	roles          roleAssigner
	mappings       mappingProvisioner
	bindings       bindingRepair
	workerID       string
	pollInterval   time.Duration
	leaseDuration  time.Duration
	batchSize      int
	now            func() time.Time
	reconciler     identityReconciler
	reconcileEvery time.Duration
	nextReconcile  time.Time
}

func (w *Worker) withReconciler(reconciler identityReconciler, every time.Duration) *Worker {
	w.reconciler = reconciler
	w.reconcileEvery = every
	return w
}

// withBindingRepair 注入平台客户绑定修复适配器（Phase 2 起）；未注入时绑定补偿任务按未配置失败。
func (w *Worker) withBindingRepair(repair bindingRepair) *Worker {
	w.bindings = repair
	return w
}

func NewWorker(store taskStore, roles roleAssigner, mappings mappingProvisioner, cfg Config) *Worker {
	return &Worker{
		store: store, roles: roles, mappings: mappings, workerID: cfg.WorkerID,
		pollInterval: cfg.PollInterval, leaseDuration: cfg.LeaseDuration,
		batchSize: cfg.BatchSize, now: func() time.Time { return time.Now().UTC() },
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || w.roles == nil || w.mappings == nil {
		return errors.New("Portal invite compensation worker dependencies are incomplete")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal invite compensation poll failed: %s", safeSummary(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	tasks, err := w.store.claim(ctx, w.workerID, w.now(), w.leaseDuration, w.batchSize)
	if err != nil {
		return 0, err
	}
	var joined error
	for i := range tasks {
		if dispatchErr := w.dispatch(ctx, tasks[i]); dispatchErr != nil {
			joined = errors.Join(joined, dispatchErr)
		}
	}
	stats, statsErr := w.store.stats(ctx)
	if statsErr != nil {
		joined = errors.Join(joined, statsErr)
	} else if len(tasks) > 0 || stats.DeadLetter > 0 {
		log.Printf("Portal invite compensation queue: claimed=%d pending=%d processing=%d retry_wait=%d dead_letter=%d", len(tasks), stats.Pending, stats.Processing, stats.RetryWait, stats.DeadLetter)
	}
	if w.reconciler != nil && (w.nextReconcile.IsZero() || !w.now().Before(w.nextReconcile)) {
		metrics, reconciliationErr := w.reconciler.RunOnce(ctx)
		w.nextReconcile = w.now().Add(w.reconcileEvery)
		if reconciliationErr != nil {
			joined = errors.Join(joined, reconciliationErr)
		} else {
			log.Printf("Portal identity reconciliation: scanned=%d consistent=%d auto_compensation=%d needs_review=%d", metrics.Scanned, metrics.Consistent, metrics.AutoCompensation, metrics.NeedsReview)
		}
	}
	return len(tasks), joined
}

type queueStats struct {
	Pending, Processing, RetryWait, DeadLetter int64
}

func (w *Worker) dispatch(ctx context.Context, task portalinvite.CompensationTask) error {
	if invalidTask(task) {
		return w.fail(ctx, task, failure{code: "INVALID_TASK", summary: "compensation task is invalid"})
	}
	switch task.TaskType {
	case portalinvite.CompensationRole:
		// 先完成平台角色，再由 Store 在同一落账事务派生映射任务，保证补偿链可从数据库恢复。
		if err := w.roles.AssignPortalRole(ctx, task); err != nil {
			return w.fail(ctx, task, failure{code: "PLATFORM_ROLE_ASSIGN_UNAVAILABLE", summary: "platform role assignment is unavailable"})
		}
		return w.store.completeRole(ctx, task, w.workerID, w.now())
	case portalinvite.CompensationMapping:
		// 远端以稳定任务身份保证幂等；本地只在仍持租约时接受结果，拒绝旧副本迟到提交。
		mapping, err := w.mappings.ProvisionMapping(ctx, task)
		if err != nil {
			return w.fail(ctx, task, failure{code: "PORTAL_MAPPING_RETRY_FAILED", summary: "Portal mapping retry failed"})
		}
		if strings.TrimSpace(mapping.PortalAccountID) == "" {
			return w.fail(ctx, task, failure{code: "PORTAL_MAPPING_INVALID_RESPONSE", summary: "Portal mapping response is invalid"})
		}
		return w.store.completeMapping(ctx, task, w.workerID, mapping, w.now())
	case portalinvite.CompensationBinding, portalinvite.CompensationBindingDisable:
		if w.bindings == nil {
			return w.fail(ctx, task, failure{code: "BINDING_REPAIR_NOT_CONFIGURED", summary: "platform customer binding repair is not configured"})
		}
		if err := w.bindings.RepairBinding(ctx, task); err != nil {
			return w.fail(ctx, task, failure{code: "PLATFORM_BINDING_RETRY_FAILED", summary: "platform customer binding retry failed"})
		}
		return w.store.completeBinding(ctx, task, w.workerID, w.now())
	default:
		return w.fail(ctx, task, failure{code: "UNKNOWN_TASK_TYPE", summary: "compensation task type is unsupported"})
	}
}

func invalidTask(task portalinvite.CompensationTask) bool {
	return strings.TrimSpace(task.TenantID) == "" || strings.TrimSpace(task.TaskNo) == "" ||
		task.CustomerID == 0 || task.ContactID == 0 || strings.TrimSpace(task.PlatformUserID) == "" ||
		strings.TrimSpace(task.AccountNo) == ""
}

func (w *Worker) fail(ctx context.Context, task portalinvite.CompensationTask, value failure) error {
	if err := w.store.failed(ctx, task, w.workerID, w.now(), value); err != nil {
		return errors.Join(errors.New(value.summary), err)
	}
	return errors.New(value.summary)
}

func safeSummary(err error) string {
	if errors.Is(err, context.Canceled) {
		return context.Canceled.Error()
	}
	if errors.Is(err, errLeaseLost) {
		return errLeaseLost.Error()
	}
	return "Portal invite compensation processing failed"
}

type failure struct{ code, summary string }

var retryDelays = [...]time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}

func failurePlan(now time.Time, completedAttempts uint8) (string, *time.Time) {
	// 首次执行后最多再退避六次；第七次失败保留为死信，避免永久故障无限占用轮询容量。
	if completedAttempts > 0 && int(completedAttempts) <= len(retryDelays) {
		next := now.Add(retryDelays[completedAttempts-1])
		return portalinvite.CompensationRetryWait, &next
	}
	return portalinvite.CompensationDeadLetter, nil
}
