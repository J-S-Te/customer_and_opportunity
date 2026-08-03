package portalfeedbackworker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/feedback"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const leaseName = "portal-feedback-sla-escalation"

type App struct {
	db     *gorm.DB
	config Config
	now    func() time.Time
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &App{db: db, config: config, now: time.Now}, nil
}

func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (a *App) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.config.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal feedback SLA scan failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) RunOnce(ctx context.Context) (int, error) {
	now := a.now().UTC()
	acquired, err := a.acquireLease(ctx, now)
	if err != nil || !acquired {
		return 0, err
	}
	total := 0
	for {
		// 同一轮固定 now，确保跨批次的 SLA 截止判断一致，不因长扫描让后批记录被提前纳入。
		processed, scanErr := a.scanBatch(ctx, now)
		total += processed
		if scanErr != nil || processed < a.config.BatchSize {
			return total, scanErr
		}
	}
}

func (a *App) acquireLease(ctx context.Context, now time.Time) (bool, error) {
	acquired := false
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT IGNORE INTO portal_feedback_job_leases(job_name,owner_id,lease_until,updated_at) VALUES(?,?,?,?)`, leaseName, a.config.WorkerID, now.Add(a.config.LeaseDuration), now).Error; err != nil {
			return err
		}
		var lease struct {
			OwnerID    string    `gorm:"column:owner_id"`
			LeaseUntil time.Time `gorm:"column:lease_until"`
		}
		if err := tx.Table("portal_feedback_job_leases").Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_name=?", leaseName).Take(&lease).Error; err != nil {
			return err
		}
		if lease.OwnerID != a.config.WorkerID && lease.LeaseUntil.After(now) {
			return nil
		}
		result := tx.Table("portal_feedback_job_leases").Where("job_name=?", leaseName).Updates(map[string]any{"owner_id": a.config.WorkerID, "lease_until": now.Add(a.config.LeaseDuration), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		acquired = result.RowsAffected == 1
		return nil
	})
	return acquired, err
}

func (a *App) scanBatch(ctx context.Context, now time.Time) (int, error) {
	processed := 0
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 反馈、升级记录、站内通知和 Outbox 在同一事务产生；唯一冲突使重复扫描保持幂等。
		var items []feedback.Feedback
		query := `SELECT f.* FROM portal_feedbacks f
WHERE f.tenant_id=? AND f.deleted_at IS NULL AND f.first_responded_at IS NULL
  AND f.first_response_due_at<=? AND f.status IN ('SUBMITTED','ACCEPTED')
  AND NOT EXISTS (
    SELECT 1 FROM portal_feedback_escalations e
    WHERE e.tenant_id=f.tenant_id AND e.feedback_id=f.id AND e.level=1
  )
ORDER BY f.first_response_due_at,f.id LIMIT ? FOR UPDATE SKIP LOCKED`
		if err := tx.Raw(query, a.config.TenantID, now, a.config.BatchSize).Scan(&items).Error; err != nil {
			return err
		}
		for i := range items {
			item := &items[i]
			escalation := feedback.Escalation{TenantID: item.TenantID, FeedbackID: item.ID, Level: 1, Reason: "FIRST_RESPONSE_OVERDUE", SentAt: now}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&escalation)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			notification := feedback.Notification{TenantID: item.TenantID, FeedbackID: item.ID, Kind: "FEEDBACK_SLA_OVERDUE", Status: "UNREAD", CreatedAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&notification).Error; err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"feedback_id": item.PublicID, "feedback_no": item.FeedbackNo, "level": 1, "reason": "FIRST_RESPONSE_OVERDUE"})
			outbox := feedback.Outbox{EventID: request.NewID(), TenantID: item.TenantID, EventType: "PORTAL_FEEDBACK_SLA_ESCALATED", AggregateID: item.ID, Payload: payload, Status: "PENDING", CreatedAt: now}
			if err := tx.Create(&outbox).Error; err != nil {
				return err
			}
			processed++
		}
		return nil
	})
	return processed, err
}
