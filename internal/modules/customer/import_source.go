package customer

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImportSource 是临时导入源的持久化意图。业务
// 历史保留在现有任务、行及审计表中；仅在网关确认
// 精确绑定已释放后，才丢弃源文件密文。
type ImportSource struct {
	ID            uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID      string     `gorm:"size:64;not null"`
	JobID         uint64     `gorm:"not null"`
	JobNo         string     `gorm:"size:64;not null"`
	ActorID       string     `gorm:"size:64;not null"`
	RequestID     string     `gorm:"size:128;not null"`
	Filename      string     `gorm:"size:100;not null"`
	ContentCipher []byte     `gorm:"type:mediumblob"`
	FileID        string     `gorm:"size:64;not null"`
	Status        string     `gorm:"size:16;not null"`
	Attempts      uint32     `gorm:"not null"`
	NextAttemptAt time.Time  `gorm:"precision:3;not null"`
	LockedBy      string     `gorm:"size:64;not null"`
	LockedUntil   *time.Time `gorm:"precision:3"`
	LastErrorCode string     `gorm:"size:32;not null"`
	CreatedAt     time.Time  `gorm:"precision:3;not null"`
	UpdatedAt     time.Time  `gorm:"precision:3;not null"`
}

func (ImportSource) TableName() string { return "crm_customer_import_sources" }

type ImportSourceRepository interface {
	CreateImportSource(context.Context, *ImportSource) error
	ClaimImportSource(context.Context, string, string, time.Time, time.Time) (*ImportSource, *ImportJob, error)
	SaveImportSource(context.Context, *ImportSource, string, time.Time) error
}

// DurableImportFileGateway 保留精确的源请求身份以供重放。
// ResolveImport 无需上传过期源文件即可获得文件 ID，
// 因此释放墓碑也可阻止旧工作器的延迟上传。
type DurableImportFileGateway interface {
	ImportFileGateway
	UploadImport(context.Context, *ImportSource, []byte) (string, error)
	ResolveImport(context.Context, *ImportSource, []byte) (string, error)
	ReleaseImport(context.Context, *ImportSource) error
}

func (r *GORMRepository) CreateImportSource(ctx context.Context, source *ImportSource) error {
	return database.FromContext(ctx, r.db).Create(source).Error
}

func (r *GORMRepository) ClaimImportSource(ctx context.Context, tenantID, token string, now, until time.Time) (*ImportSource, *ImportJob, error) {
	var source ImportSource
	var job ImportJob
	err := database.WithTransaction(ctx, r.db, func(txCtx context.Context) error {
		db := database.FromContext(txCtx, r.db)
		// 部署只能排空所配置租户的任务。SKIP LOCKED 允许
		// 其他 API 副本恢复独立任务，同时避免重复认领。
		err := db.Table("crm_customer_import_sources AS src").Select("src.*").
			Joins("JOIN crm_customer_import_jobs AS job ON job.tenant_id=src.tenant_id AND job.id=src.job_id AND job.job_no=src.job_no AND job.actor_id=src.actor_id").
			Where("src.tenant_id=? AND src.status<>'RELEASED' AND src.next_attempt_at<=? AND (src.locked_until IS NULL OR src.locked_until<=?)", tenantID, now, now).
			Where("src.status='PENDING' OR job.status='COMPLETED' OR job.expires_at<=?", now).
			Order("src.id ASC").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&source).Error
		if err != nil {
			return err
		}
		if err = db.Where("tenant_id=? AND id=? AND actor_id=? AND job_no=?", tenantID, source.JobID, source.ActorID, source.JobNo).Take(&job).Error; err != nil {
			return err
		}
		result := db.Model(&ImportSource{}).Where("id=? AND tenant_id=?", source.ID, tenantID).
			Updates(map[string]any{"locked_by": token, "locked_until": until, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrImportJobConflict
		}
		source.LockedBy, source.LockedUntil = token, &until
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &source, &job, nil
}

func (r *GORMRepository) SaveImportSource(ctx context.Context, source *ImportSource, token string, now time.Time) error {
	result := database.FromContext(ctx, r.db).Model(&ImportSource{}).
		Where("id=? AND tenant_id=? AND job_id=? AND actor_id=? AND job_no=? AND locked_by=? AND locked_until>?", source.ID, source.TenantID, source.JobID, source.ActorID, source.JobNo, token, now).
		Updates(map[string]any{"status": source.Status, "file_id": source.FileID, "content_cipher": source.ContentCipher,
			"attempts": source.Attempts, "next_attempt_at": source.NextAttemptAt, "last_error_code": source.LastErrorCode,
			"locked_by": "", "locked_until": nil, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImportJobConflict
	}
	return nil
}

// ProcessImportSource 最多执行一个已持久化意图。authorize
// 由生产装配入口提供，在每次外部调用前立即重检；
// 清理使用基础服务商业授权操作。
func (s *Service) ProcessImportSource(ctx context.Context, tenantID string, authorize func(context.Context, bool) error) (bool, error) {
	gateway, ok := s.importGateway.(DurableImportFileGateway)
	if !ok || s.importSources == nil || s.codec == nil || tenantID == "" || authorize == nil {
		return false, ErrImportGatewayUnavailable
	}
	now := s.now()
	token := requestctx.NewID()
	source, job, err := s.importSources.ClaimImportSource(ctx, tenantID, token, now, now.Add(2*time.Minute))
	if err != nil || source == nil {
		return false, err
	}
	if job == nil || source.TenantID != tenantID || job.TenantID != tenantID || source.JobID != job.ID || source.JobNo != job.JobNo || source.ActorID != job.ActorID || source.ActorID == "" || !validImportJobNo(source.JobNo) || source.RequestID != "crm-customer-import-"+source.JobNo {
		return true, ErrImportJobConflict
	}
	release := job.Status == "COMPLETED" || !job.ExpiresAt.After(now)
	finish := func(cause error, code string) (bool, error) {
		finishedAt := s.now()
		source.Attempts++
		source.LastErrorCode = code
		if cause != nil {
			shift := source.Attempts
			if shift > 6 {
				shift = 6
			}
			source.NextAttemptAt = finishedAt.Add(time.Duration(1<<shift) * 15 * time.Second)
		} else {
			source.NextAttemptAt = finishedAt
		}
		return true, errors.Join(cause, s.importSources.SaveImportSource(ctx, source, token, finishedAt))
	}
	if err = authorize(ctx, release); err != nil {
		return finish(err, "AUTHORIZATION_DENIED")
	}
	if source.FileID == "" {
		content, decryptErr := s.codec.Decrypt(source.ContentCipher)
		if decryptErr != nil || len(content) == 0 || len(content) > importMaxFileBytes {
			return finish(ErrImportInvalidFile, "SOURCE_UNAVAILABLE")
		}
		if release {
			source.FileID, err = gateway.ResolveImport(ctx, source, []byte(content))
		} else {
			source.FileID, err = gateway.UploadImport(ctx, source, []byte(content))
		}
		if err != nil || source.FileID == "" {
			return finish(ErrImportGatewayUnavailable, "GATEWAY_UNAVAILABLE")
		}
	}
	if release {
		if err = authorize(ctx, true); err != nil {
			return finish(err, "AUTHORIZATION_DENIED")
		}
		if err = gateway.ReleaseImport(ctx, source); err != nil {
			return finish(ErrImportGatewayUnavailable, "GATEWAY_UNAVAILABLE")
		}
		source.Status, source.ContentCipher = "RELEASED", nil
	} else {
		source.Status = "UPLOADED"
	}
	return finish(nil, "")
}
