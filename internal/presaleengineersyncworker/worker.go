package presaleengineersyncworker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

type workerStore interface {
	Schedule(context.Context, time.Time, time.Duration) error
	Claim(context.Context, string, time.Time, time.Duration, int) ([]presale.EngineerSyncJob, error)
	Renew(context.Context, presale.EngineerSyncJob, string, time.Time, time.Duration) error
	Apply(context.Context, presale.EngineerSyncJob, string, SourceSnapshot, time.Time, time.Duration) error
	Fail(context.Context, presale.EngineerSyncJob, string, time.Time, string) error
}

type Worker struct {
	store  workerStore
	source EngineerSource
	cfg    Config
	now    func() time.Time
}

func NewWorker(store workerStore, source EngineerSource, cfg Config) *Worker {
	return &Worker{store: store, source: source, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || w.source == nil {
		return errors.New("PMS engineer sync dependencies are incomplete")
	}
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("PMS engineer sync poll failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	// 调度状态与执行任务分离：先为到期租户补幂等任务，再由多副本通过租约领取，避免轮询器重复创建作业。
	now := w.now()
	if err := w.store.Schedule(ctx, now, w.cfg.SyncInterval); err != nil {
		return 0, err
	}
	jobs, err := w.store.Claim(ctx, w.cfg.WorkerID, now, w.cfg.LeaseDuration, w.cfg.BatchSize)
	if err != nil {
		return 0, err
	}
	var joined error
	for _, job := range jobs {
		// 远端全量快照可能耗时，调用前后各续租一次；Apply 仍会在事务内最终复核租约。
		if renewErr := w.store.Renew(ctx, job, w.cfg.WorkerID, w.now(), w.cfg.LeaseDuration); renewErr != nil {
			joined = errors.Join(joined, renewErr)
			continue
		}
		snapshot, fetchErr := w.source.Fetch(ctx, job.TenantID)
		if fetchErr == nil {
			fetchErr = validateSnapshot(job.TenantID, snapshot)
		}
		if fetchErr == nil {
			fetchErr = w.store.Renew(ctx, job, w.cfg.WorkerID, w.now(), w.cfg.LeaseDuration)
		}
		if fetchErr == nil {
			fetchErr = w.store.Apply(ctx, job, w.cfg.WorkerID, snapshot, w.now(), w.cfg.SyncInterval)
		}
		if fetchErr != nil && !errors.Is(fetchErr, errLeaseLost) {
			if failErr := w.store.Fail(ctx, job, w.cfg.WorkerID, w.now(), fetchErr.Error()); failErr != nil {
				fetchErr = errors.Join(fetchErr, failErr)
			}
		}
		joined = errors.Join(joined, fetchErr)
	}
	return len(jobs), joined
}

func validateSnapshot(tenant string, snapshot SourceSnapshot) error {
	// 仅接受与本地任务租户一致的完整快照；空或部分响应不能触发“未出现人员全部停用”的破坏性对账。
	if !snapshot.Full || snapshot.TenantID == "" || snapshot.TenantID != tenant || len(snapshot.Engineers) == 0 {
		return errors.New("PMS technician snapshot lacks a matching tenant-scoped full revision")
	}
	seen := map[string]bool{}
	for _, person := range snapshot.Engineers {
		if person.PersonID != strings.TrimSpace(person.PersonID) || person.PersonName != strings.TrimSpace(person.PersonName) || person.Department != strings.TrimSpace(person.Department) || person.Role != strings.TrimSpace(person.Role) || person.Contact != strings.TrimSpace(person.Contact) || person.PersonID == "" || len([]rune(person.PersonID)) > 64 || person.PersonName == "" || len([]rune(person.PersonName)) > 128 || person.Department == "" || len([]rune(person.Department)) > 128 || len([]rune(person.Contact)) > 256 || person.SyncedAt.IsZero() {
			return errors.New("PMS technician snapshot contains invalid required fields")
		}
		if seen[person.PersonID] {
			return errors.New("PMS technician snapshot contains duplicate personId")
		}
		seen[person.PersonID] = true
		if _, ok := normalizedRole(person.Role); !ok {
			return fmt.Errorf("PMS technician snapshot contains unsupported role")
		}
		if len(person.SkillTags) > 100 {
			return errors.New("PMS technician snapshot contains too many skill tags")
		}
		skills := map[string]bool{}
		for _, skill := range person.SkillTags {
			if skill != strings.TrimSpace(skill) || skill == "" || len([]rune(skill)) > 64 || skills[skill] {
				return errors.New("PMS technician snapshot contains invalid skill tag")
			}
			skills[skill] = true
		}
	}
	if len(snapshot.Engineers) > 0 && snapshot.Revision.IsZero() {
		return errors.New("PMS technician snapshot lacks a source revision")
	}
	return nil
}
