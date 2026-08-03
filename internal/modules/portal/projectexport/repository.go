package projectexport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }
func (r *GORMRepository) session(ctx context.Context) *gorm.DB {
	return database.FromContext(ctx, r.db).WithContext(ctx)
}

func (r *GORMRepository) FindByKey(ctx context.Context, actor Actor, key string) (*Job, error) {
	var value Job
	err := r.session(ctx).Where("tenant_id=? AND customer_id=? AND account_id=? AND idempotency_key=?", actor.TenantID, actor.CustomerID, actor.AccountID, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}

func (r *GORMRepository) Create(ctx context.Context, job *Job, event *Event) error {
	return r.session(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(job).Error; err != nil {
			return err
		}
		event.ExportID = job.ID
		return tx.Create(event).Error
	})
}

func (r *GORMRepository) FindOwned(ctx context.Context, actor Actor, publicID string, includeFile bool) (*Job, error) {
	var value Job
	db := r.session(ctx)
	if !includeFile {
		db = db.Omit("file_bytes", "snapshot_json")
	}
	err := db.Where("tenant_id=? AND customer_id=? AND account_id=? AND public_id=?", actor.TenantID, actor.CustomerID, actor.AccountID, publicID).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}

func (r *GORMRepository) CreateGrant(ctx context.Context, actor Actor, exportPublicID, grantPublicID string, now, expires time.Time, tokenHash string) (*Grant, error) {
	var result Grant
	err := r.session(ctx).Transaction(func(tx *gorm.DB) error {
		var job Job
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND customer_id=? AND account_id=? AND public_id=? AND status=?", actor.TenantID, actor.CustomerID, actor.AccountID, exportPublicID, StatusReady).Take(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		result = Grant{PublicID: grantPublicID, TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ExportID: job.ID, TokenHash: tokenHash, Status: grantActive, ExpiresAt: expires, CreatedAt: now}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		return tx.Create(&Event{TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ExportID: job.ID, EventType: "DOWNLOAD_GRANT_ISSUED", Result: "SUCCESS", OccurredAt: now}).Error
	})
	return &result, err
}

func (r *GORMRepository) ConsumeGrant(ctx context.Context, actor Actor, exportPublicID, tokenHash string, now time.Time, trace string) (*Job, error) {
	// 授权消费、任务读取和审计事件处于同一事务，避免文件已返回但凭据仍可再次使用。
	var result Job
	err := r.session(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tenant_id=? AND customer_id=? AND account_id=? AND public_id=? AND status=?", actor.TenantID, actor.CustomerID, actor.AccountID, exportPublicID, StatusReady).Take(&result).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		if result.FileSize <= 0 || result.FileSize > maxExportBytes || int64(len(result.FileBytes)) != result.FileSize || digestBytes(result.FileBytes) != result.FileHash {
			return ErrNotReady
		}
		var grant Grant
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND customer_id=? AND account_id=? AND export_id=? AND token_hash=? AND status=? AND expires_at>?", actor.TenantID, actor.CustomerID, actor.AccountID, result.ID, tokenHash, grantActive, now).Take(&grant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		updateResult := tx.Model(&Grant{}).Where("id=? AND status=?", grant.ID, grantActive).Updates(map[string]any{"status": grantUsed, "used_at": now})
		if updateResult.Error != nil {
			return updateResult.Error
		}
		if updateResult.RowsAffected != 1 {
			return ErrInvalidGrant
		}
		return tx.Create(&Event{TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ExportID: result.ID, EventType: "DOWNLOAD_GRANT_CONSUMED", Result: "SUCCESS", RequestTrace: trace, OccurredAt: now}).Error
	})
	return &result, err
}

func (r *GORMRepository) RecordDeliveryOutcome(ctx context.Context, actor Actor, exportID uint64, now time.Time, trace string, success bool, reason string) error {
	// 这里只记录服务端传输观察值；失败原因会规范化，避免把底层网络或文件细节写入审计。
	eventType, result := "DOWNLOAD_STREAM_WRITTEN", "SUCCESS"
	if !success {
		eventType, result = "DOWNLOAD_STREAM_FAILED", "FAILED"
	}
	return r.session(ctx).Create(&Event{TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ExportID: exportID, EventType: eventType, Result: result, ReasonCode: strings.TrimSpace(reason), RequestTrace: trace, OccurredAt: now}).Error
}
