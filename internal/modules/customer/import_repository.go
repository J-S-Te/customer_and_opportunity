package customer

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ImportRepository interface {
	CreateImportPreview(context.Context, *ImportJob, []ImportRow) error
	FindImportPreview(context.Context, string, string, string) (*ImportJob, []ImportRow, error)
	ClaimImport(context.Context, string, string, string, uint64, string, string, time.Time, time.Time) (*ImportJob, []ImportRow, error)
	CreateImportIdempotency(context.Context, *ImportIdempotency) error
	UpdateImportRow(context.Context, *ImportRow) error
	LockAndRenewImportLease(context.Context, *ImportJob, string, time.Time, time.Time) error
	CompleteImport(context.Context, *ImportJob, string, time.Time, *ImportIdempotency) error
	CompleteImportIdempotency(context.Context, *ImportIdempotency) error
	FindImportIdempotency(context.Context, string, string, string) (*ImportIdempotency, error)
}

func (r *GORMRepository) LockAndRenewImportLease(ctx context.Context, job *ImportJob, lockToken string, now, lockedUntil time.Time) error {
	db := database.FromContext(ctx, r.db)
	var locked ImportJob
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND tenant_id=? AND actor_id=?", job.ID, job.TenantID, job.ActorID).Take(&locked).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrImportJobNotFound
	}
	if err != nil {
		return err
	}
	if !validImportLease(&locked, job, lockToken, now) {
		return ErrImportJobConflict
	}
	result := db.Model(&ImportJob{}).
		Where("id=? AND tenant_id=? AND actor_id=? AND status='COMMITTING' AND locked_by=? AND version=?", job.ID, job.TenantID, job.ActorID, lockToken, job.Version).
		Updates(map[string]any{"locked_until": lockedUntil, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImportJobConflict
	}
	job.LockedUntil, job.UpdatedAt = &lockedUntil, now
	return nil
}

func validImportLease(locked, claimed *ImportJob, lockToken string, now time.Time) bool {
	return locked != nil && claimed != nil && locked.ID == claimed.ID && locked.TenantID == claimed.TenantID && locked.ActorID == claimed.ActorID &&
		locked.Status == "COMMITTING" && locked.LockedBy == lockToken && locked.Version == claimed.Version && locked.LockedUntil != nil && locked.LockedUntil.After(now)
}

func (r *GORMRepository) CreateImportIdempotency(ctx context.Context, model *ImportIdempotency) error {
	return database.FromContext(ctx, r.db).Create(model).Error
}

func (r *GORMRepository) CreateImportPreview(ctx context.Context, job *ImportJob, rows []ImportRow) error {
	db := database.FromContext(ctx, r.db)
	if err := db.Create(job).Error; err != nil {
		return err
	}
	for index := range rows {
		rows[index].JobID = job.ID
	}
	if len(rows) == 0 {
		return nil
	}
	return db.Create(&rows).Error
}

func (r *GORMRepository) FindImportPreview(ctx context.Context, tenantID, actorID, jobNo string) (*ImportJob, []ImportRow, error) {
	var job ImportJob
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND actor_id=? AND job_no=?", tenantID, actorID, jobNo).
		Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrImportJobNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var rows []ImportRow
	if err = database.FromContext(ctx, r.db).
		Where("tenant_id=? AND job_id=?", tenantID, job.ID).
		Order("row_no ASC").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	return &job, rows, nil
}

func (r *GORMRepository) ClaimImport(ctx context.Context, tenantID, actorID, jobNo string, version uint64, idempotencyKey, lockToken string, now, lockedUntil time.Time) (*ImportJob, []ImportRow, error) {
	db := database.FromContext(ctx, r.db)
	var job ImportJob
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND actor_id=? AND job_no=?", tenantID, actorID, jobNo).
		Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrImportJobNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if job.Status == "PREVIEWED" && !job.ExpiresAt.After(now) {
		return nil, nil, ErrImportJobExpired
	}
	if err = validateImportClaim(&job, version, now); err != nil {
		return nil, nil, err
	}
	previousVersion := job.Version
	result := db.Model(&ImportJob{}).
		Where("id=? AND tenant_id=? AND actor_id=? AND version=? AND ((status='PREVIEWED' AND expires_at>?) OR (status='COMMITTING' AND (locked_until IS NULL OR locked_until<=?)))", job.ID, tenantID, actorID, previousVersion, now, now).
		Updates(map[string]any{"status": "COMMITTING", "commit_request_version": version, "commit_idempotency_key": idempotencyKey, "locked_by": lockToken, "locked_until": lockedUntil, "updated_at": now, "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, ErrImportJobConflict
	}
	job.Status, job.Version, job.UpdatedAt, job.CommitRequestVersion, job.CommitIdempotencyKey, job.LockedBy, job.LockedUntil = "COMMITTING", previousVersion+1, now, version, idempotencyKey, lockToken, &lockedUntil
	var rows []ImportRow
	if err = db.Where("tenant_id=? AND job_id=?", tenantID, job.ID).Order("row_no ASC").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	return &job, rows, nil
}

// validateImportClaim deliberately does not bind an expired lease takeover to
// the previous browser's idempotency key. The same tenant actor and original
// preview version may resume with a new key after a browser refresh; the new
// lock token/version fences the old worker, while either key can later rebuild
// the same durable COMPLETED response.
func validateImportClaim(job *ImportJob, requestVersion uint64, now time.Time) error {
	if job == nil {
		return ErrImportJobNotFound
	}
	switch job.Status {
	case "PREVIEWED":
		if !job.ExpiresAt.After(now) {
			return ErrImportJobExpired
		}
		if job.Version != requestVersion {
			return ErrVersionConflict
		}
		return nil
	case "COMMITTING":
		if job.LockedUntil != nil && job.LockedUntil.After(now) {
			return ErrImportJobConflict
		}
		if job.CommitRequestVersion != requestVersion {
			return ErrIdempotencyConflict
		}
		return nil
	default:
		return ErrImportJobConflict
	}
}

func (r *GORMRepository) UpdateImportRow(ctx context.Context, row *ImportRow) error {
	updates := map[string]any{
		"status": row.Status, "error_column": row.ErrorColumn, "error_code": row.ErrorCode,
		"error_message": row.ErrorMessage, "customer_id": row.CustomerID,
		"customer_no": row.CustomerNo, "command_cipher": row.CommandCipher, "updated_at": row.UpdatedAt,
	}
	result := database.FromContext(ctx, r.db).Model(&ImportRow{}).
		Where("id=? AND tenant_id=? AND job_id=?", row.ID, row.TenantID, row.JobID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImportJobConflict
	}
	return nil
}

func (r *GORMRepository) CompleteImport(ctx context.Context, job *ImportJob, lockToken string, now time.Time, idempotency *ImportIdempotency) error {
	db := database.FromContext(ctx, r.db)
	result := db.Model(&ImportJob{}).
		Where("id=? AND tenant_id=? AND actor_id=? AND status='COMMITTING' AND locked_by=? AND version=? AND locked_until>?", job.ID, job.TenantID, job.ActorID, lockToken, job.Version-1, now).
		Updates(map[string]any{
			"status": "COMPLETED", "succeeded_rows": job.SucceededRows, "failed_rows": job.FailedRows,
			"completed_at": job.CompletedAt, "updated_at": job.UpdatedAt, "version": job.Version, "locked_by": "", "locked_until": nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImportJobConflict
	}
	result = db.Model(&ImportIdempotency{}).
		Where("tenant_id=? AND actor_id=? AND idempotency_key=? AND request_hash=? AND status='PROCESSING'", idempotency.TenantID, idempotency.ActorID, idempotency.Key, idempotency.RequestHash).
		Updates(map[string]any{"status": "COMPLETED", "response_json": idempotency.ResponseJSON})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImportJobConflict
	}
	return nil
}

func (r *GORMRepository) CompleteImportIdempotency(ctx context.Context, model *ImportIdempotency) error {
	result := database.FromContext(ctx, r.db).Model(&ImportIdempotency{}).
		Where("tenant_id=? AND actor_id=? AND idempotency_key=? AND request_hash=? AND status='PROCESSING'", model.TenantID, model.ActorID, model.Key, model.RequestHash).
		Updates(map[string]any{"status": "COMPLETED", "response_json": model.ResponseJSON})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImportJobConflict
	}
	return nil
}

func (r *GORMRepository) FindImportIdempotency(ctx context.Context, tenantID, actorID, key string) (*ImportIdempotency, error) {
	var model ImportIdempotency
	err := database.FromContext(ctx, r.db).
		Where("tenant_id=? AND actor_id=? AND idempotency_key=?", tenantID, actorID, key).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &model, err
}
