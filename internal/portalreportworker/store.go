package portalreportworker

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

// deadLetterProjection 在同一事务内标记提交失败，或确认已受理申请应收口成功。
// 只声明写终态所需的最小能力，Worker 仍以 Outbox 事件身份作为幂等依据。
type deadLetterProjection interface {
	MarkDeliveryDeadLetter(context.Context, report.Outbox) error
}

type outboxStore struct {
	db          *gorm.DB
	deadLetters deadLetterProjection
	// transaction 与 updateOutbox 只在测试中替换；生产装配始终使用同一 GORM 事务与同一条更新语句。
	transaction  func(context.Context, func(context.Context) error) error
	updateOutbox func(context.Context, report.Outbox, string, map[string]any) error
}

func newOutboxStore(db *gorm.DB, deadLetters deadLetterProjection) *outboxStore {
	return &outboxStore{db: db, deadLetters: deadLetters}
}

// 领取事务只负责建立有限租约，提交后才调用项目系统；数据库行锁不会跨越外部 HTTP。
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
			"status": report.OutboxProcessing, "locked_by": workerID, "locked_until": lockedUntil,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errLeaseLost
		}
		for i := range events {
			events[i].Status, events[i].LockedBy, events[i].LockedUntil = report.OutboxProcessing, workerID, &lockedUntil
		}
		claimed = events
		return nil
	})
	return claimed, err
}

func (s *outboxStore) sent(ctx context.Context, event report.Outbox, workerID string, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&report.Outbox{}).
		Where("id=? AND status=? AND locked_by=?", event.ID, report.OutboxProcessing, workerID).
		Updates(map[string]any{"status": report.OutboxSent, "sent_at": now, "locked_by": "", "locked_until": nil, "next_retry_at": nil, "last_error_summary": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (s *outboxStore) failed(ctx context.Context, event report.Outbox, workerID string, now time.Time, summary string) error {
	// 有限退避耗尽后转死信；错误摘要先清洗，避免上游响应或凭据污染持久化诊断字段。
	attempt := event.RetryCount + 1
	status, next := outboxFailurePlan(now, attempt)
	fields := map[string]any{
		"status": status, "retry_count": attempt, "next_retry_at": next,
		"locked_by": "", "locked_until": nil, "last_error_summary": sanitize(summary),
	}
	if status == report.OutboxDeadLetter {
		return s.commitDeadLetter(ctx, event, workerID, fields)
	}
	return s.writeFailure(ctx, event, workerID, fields)
}

// commitDeadLetter 将投递终止与领域投影共同提交，已受理申请直接收口SENT。
// 参数：ctx为任务上下文，event为租约事件，workerID为持有者，fields为失败字段。
// 返回值：无；身份、租约或写入失败时整体回滚，不回退已推进业务状态。
func (s *outboxStore) commitDeadLetter(ctx context.Context, event report.Outbox, workerID string, fields map[string]any) error {
	if event.AggregateID == 0 {
		// 结构无效的 Outbox 没有可绑定的申请，只能落死信交由运维清理，避免租约过期后无限重投。
		return s.writeFailure(ctx, event, workerID, fields)
	}
	if s.deadLetters == nil {
		return errors.New("Portal report dead-letter projection is not configured")
	}
	run := s.transaction
	if run == nil {
		run = func(ctx context.Context, fn func(context.Context) error) error {
			return database.WithTransaction(ctx, s.db, fn)
		}
	}
	return run(ctx, func(txCtx context.Context) error {
		if err := s.deadLetters.MarkDeliveryDeadLetter(txCtx, event); err != nil {
			if !errors.Is(err, report.ErrDeliveryAlreadyAccepted) {
				return err
			}
			fields["status"], fields["sent_at"] = report.OutboxSent, time.Now().UTC()
			fields["next_retry_at"], fields["last_error_summary"] = nil, ""
		}
		return s.writeFailure(txCtx, event, workerID, fields)
	})
}

func (s *outboxStore) writeFailure(ctx context.Context, event report.Outbox, workerID string, fields map[string]any) error {
	if s.updateOutbox != nil {
		return s.updateOutbox(ctx, event, workerID, fields)
	}
	result := database.FromContext(ctx, s.db).Model(&report.Outbox{}).
		Where("id=? AND status=? AND locked_by=?", event.ID, report.OutboxProcessing, workerID).
		Updates(fields)
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
		return report.OutboxRetryWait, &next
	}
	return report.OutboxDeadLetter, nil
}
