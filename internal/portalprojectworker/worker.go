package portalprojectworker

import (
	"context"
	"errors"
	"log"
	"time"
)

type stateStore interface {
	seedCustomers(context.Context, string, time.Time) error
	claim(context.Context, string, string, time.Time, time.Duration) (*syncState, error)
	applyPage(context.Context, *syncState, string, sourcePage, time.Time, bool, time.Duration, time.Duration) (int, error)
	failed(context.Context, *syncState, string, time.Time, time.Duration, string) error
}
type projectSource interface {
	changed(context.Context, string, uint64, string) (sourcePage, error)
}

type Worker struct {
	store         stateStore
	source        projectSource
	tenantID      string
	workerID      string
	pollInterval  time.Duration
	syncInterval  time.Duration
	leaseDuration time.Duration
	retryInterval time.Duration
	now           func() time.Time
}

func newWorker(store stateStore, source projectSource, cfg Config) *Worker {
	return &Worker{store: store, source: source, tenantID: cfg.TenantID, workerID: cfg.WorkerID, pollInterval: cfg.PollInterval, syncInterval: cfg.SyncInterval, leaseDuration: cfg.LeaseDuration, retryInterval: cfg.RetryInterval, now: func() time.Time { return time.Now().UTC() }}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || w.source == nil {
		return errors.New("Portal project worker dependencies are incomplete")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal project worker poll failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	// 每轮先发现新授权客户，使新增 Portal 映射无需重启 Worker 即可进入同步队列。
	now := w.now()
	if err := w.store.seedCustomers(ctx, w.tenantID, now); err != nil {
		return 0, err
	}
	state, err := w.store.claim(ctx, w.tenantID, w.workerID, now, w.leaseDuration)
	if err != nil || state == nil {
		return 0, err
	}
	updated := 0
	for {
		// 游标页逐页落账并续租；源端失败只让当前客户退避，不会回滚已经成功提交的前序页。
		page, fetchErr := w.source.changed(ctx, state.TenantID, state.CustomerID, state.Cursor)
		if fetchErr != nil {
			if projectionErr := w.store.failed(ctx, state, w.workerID, w.now(), w.retryInterval, fetchErr.Error()); projectionErr != nil {
				return updated, errors.Join(fetchErr, projectionErr)
			}
			return updated, fetchErr
		}
		count, applyErr := w.store.applyPage(ctx, state, w.workerID, page, w.now(), !page.HasMore, w.syncInterval, w.leaseDuration)
		updated += count
		if applyErr != nil {
			return updated, applyErr
		}
		if !page.HasMore {
			return updated, nil
		}
	}
}
