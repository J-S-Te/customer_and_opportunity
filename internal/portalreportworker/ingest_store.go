package portalreportworker

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"gorm.io/gorm"
)

type ingestJobStore struct{ db *gorm.DB }

func newIngestJobStore(db *gorm.DB) *ingestJobStore { return &ingestJobStore{db: db} }

func (s *ingestJobStore) claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]report.IngestJob, error) {
	var claimed []report.IngestJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 过期租约允许接管，稳定 EventID 则把可能重复的文件摄取约束为幂等操作。
		var jobs []report.IngestJob
		if err := tx.Raw(`SELECT * FROM portal_report_ingest_jobs
WHERE ((status IN ('PENDING','RETRY_WAIT') AND (next_retry_at IS NULL OR next_retry_at <= ?))
       OR (status = 'PROCESSING' AND locked_until < ?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, limit).Scan(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(jobs))
		for i := range jobs {
			ids = append(ids, jobs[i].ID)
		}
		lockedUntil := now.Add(lease)
		result := tx.Model(&report.IngestJob{}).Where("id IN ?", ids).Updates(map[string]any{"status": report.IngestProcessing, "locked_by": workerID, "locked_until": lockedUntil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errLeaseLost
		}
		for i := range jobs {
			jobs[i].Status, jobs[i].LockedBy, jobs[i].LockedUntil = report.IngestProcessing, workerID, &lockedUntil
		}
		claimed = jobs
		return nil
	})
	return claimed, err
}

func (s *ingestJobStore) failed(ctx context.Context, job report.IngestJob, workerID string, now time.Time, summary string) error {
	attempt := job.RetryCount + 1
	status, next := outboxFailurePlan(now, attempt)
	result := s.db.WithContext(ctx).Model(&report.IngestJob{}).
		Where("id=? AND status=? AND locked_by=?", job.ID, report.IngestProcessing, workerID).
		Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}
