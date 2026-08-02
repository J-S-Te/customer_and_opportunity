package portalreportworker

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"gorm.io/gorm"
)

type outboxStore struct{ db *gorm.DB }

func newOutboxStore(db *gorm.DB) *outboxStore { return &outboxStore{db: db} }

// claim holds row locks only while establishing a finite processing lease.
// External HTTP is always called after this transaction has committed.
func (s *outboxStore) claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]report.Outbox, error) {
	var claimed []report.Outbox
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []report.Outbox
		if err := tx.Raw(`SELECT * FROM portal_report_outbox
WHERE event_type = 'PORTAL_REPORT_SUBMITTED'
  AND ((status IN ('PENDING','RETRY_WAIT') AND (next_retry_at IS NULL OR next_retry_at <= ?))
       OR (status = 'PROCESSING' AND locked_until < ?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, limit).Scan(&events).Error; err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(events))
		for i := range events {
			ids = append(ids, events[i].ID)
		}
		lockedUntil := now.Add(lease)
		result := tx.Model(&report.Outbox{}).Where("id IN ?", ids).Updates(map[string]any{
			"status": "PROCESSING", "locked_by": workerID, "locked_until": lockedUntil,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errLeaseLost
		}
		for i := range events {
			events[i].Status, events[i].LockedBy, events[i].LockedUntil = "PROCESSING", workerID, &lockedUntil
		}
		claimed = events
		return nil
	})
	return claimed, err
}

func (s *outboxStore) sent(ctx context.Context, event report.Outbox, workerID string, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&report.Outbox{}).
		Where("id=? AND status='PROCESSING' AND locked_by=?", event.ID, workerID).
		Updates(map[string]any{"status": "SENT", "sent_at": now, "locked_by": "", "locked_until": nil, "next_retry_at": nil, "last_error_summary": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (s *outboxStore) failed(ctx context.Context, event report.Outbox, workerID string, now time.Time, summary string) error {
	attempt := event.RetryCount + 1
	status, next := outboxFailurePlan(now, attempt)
	result := s.db.WithContext(ctx).Model(&report.Outbox{}).
		Where("id=? AND status='PROCESSING' AND locked_by=?", event.ID, workerID).
		Updates(map[string]any{
			"status": status, "retry_count": attempt, "next_retry_at": next,
			"locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

var retryDelays = [...]time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}

func outboxFailurePlan(now time.Time, attempt uint8) (string, *time.Time) {
	if attempt > 0 && int(attempt) <= len(retryDelays) {
		next := now.Add(retryDelays[attempt-1])
		return "RETRY_WAIT", &next
	}
	return "DEAD_LETTER", nil
}
