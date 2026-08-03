package presaleworker

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type outboxStore struct{ db *gorm.DB }

func newOutboxStore(db *gorm.DB) *outboxStore { return &outboxStore{db: db} }

func heartbeat(tx *gorm.DB, workerID string, now time.Time) error {
	value := presale.WorkerHeartbeat{WorkerType: presale.PresaleDeliveryWorkerType, WorkerID: workerID, HeartbeatAt: now}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "worker_type"}, {Name: "worker_id"}},
		DoUpdates: clause.Assignments(map[string]any{"heartbeat_at": now, "updated_at": now}),
	}).Create(&value).Error
}

func (s *outboxStore) heartbeat(ctx context.Context, workerID string, now time.Time) error {
	return heartbeat(s.db.WithContext(ctx), workerID, now)
}

// 领取使用短事务和 SKIP LOCKED；任何审批或 PMS HTTP 调用都在事务提交后执行，不长期占用 Outbox 行锁。
func (s *outboxStore) claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]presale.OutboxEvent, error) {
	var result []presale.OutboxEvent
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []presale.OutboxEvent
		err := tx.Raw(`SELECT * FROM crm_outbox_events
WHERE event_type IN ('PRESALE_APPROVAL_START_REQUESTED','PRESALE_APPROVAL_ACTION_REQUESTED','PRESALE_WORKLOG_CREATED')
  AND ((status IN ('PENDING','RETRY_WAIT') AND (next_retry_at IS NULL OR next_retry_at <= ?))
       OR (status = 'PROCESSING' AND locked_until < ?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, limit).Scan(&events).Error
		if err != nil {
			return err
		}
		// 心跳与领取在同一事务提交；心跳失败会回滚全部租约变化，不能出现“已领取但监控认为实例未存活”的状态。
		if err = heartbeat(tx, workerID, now); err != nil {
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
		if err = tx.Model(&presale.OutboxEvent{}).Where("id IN ?", ids).Updates(map[string]any{"status": "PROCESSING", "locked_by": workerID, "locked_until": lockedUntil}).Error; err != nil {
			return err
		}
		for i := range events {
			events[i].Status, events[i].LockedBy, events[i].LockedUntil = "PROCESSING", workerID, &lockedUntil
		}
		result = events
		return nil
	})
	return result, err
}

func (s *outboxStore) sent(ctx context.Context, event presale.OutboxEvent, workerID string, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&presale.OutboxEvent{}).
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

func (s *outboxStore) failed(ctx context.Context, event presale.OutboxEvent, workerID string, now time.Time, summary string) error {
	// 失败按有限退避推进，耗尽后转死信；状态条件阻止不再持有事件的副本覆盖结果。
	attempt := event.RetryCount + 1
	status, next := outboxFailurePlan(now, attempt)
	result := s.db.WithContext(ctx).Model(&presale.OutboxEvent{}).
		Where("id=? AND status='PROCESSING' AND locked_by=?", event.ID, workerID).
		Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func outboxFailurePlan(now time.Time, attempt uint8) (string, *time.Time) {
	if retryAt, ok := outboxRetryAt(now, attempt); ok {
		return "RETRY_WAIT", retryAt
	}
	return "DEAD_LETTER", nil
}

var outboxRetryDelays = [...]time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}

func outboxRetryAt(now time.Time, attempt uint8) (*time.Time, bool) {
	if attempt == 0 || int(attempt) > len(outboxRetryDelays) {
		return nil, false
	}
	next := now.Add(outboxRetryDelays[attempt-1])
	return &next, true
}
