package projectexportworker

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectexport"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errLeaseLost = errors.New("project export render lease was lost")

type GORMStore struct{ db *gorm.DB }

func NewGORMStore(db *gorm.DB) *GORMStore { return &GORMStore{db: db} }

func (s *GORMStore) Claim(ctx context.Context, workerID string, now time.Time, lease time.Duration) (*projectexport.Job, error) {
	var claimed *projectexport.Job
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job projectexport.Job
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status=? OR (status=? AND locked_until<?)", projectexport.StatusPending, projectexport.StatusGenerating, now).
			Order("created_at,id").Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		until := now.Add(lease)
		result := tx.Model(&projectexport.Job{}).
			Where("id=? AND version=? AND (status=? OR (status=? AND locked_until<?))", job.ID, job.Version, projectexport.StatusPending, projectexport.StatusGenerating, now).
			Updates(map[string]any{"status": projectexport.StatusGenerating, "locked_by": workerID, "locked_until": until, "updated_at": now, "version": gorm.Expr("version+1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		job.Status, job.LockedBy, job.LockedUntil, job.Version = projectexport.StatusGenerating, workerID, &until, job.Version+1
		claimed = &job
		return tx.Create(&projectexport.Event{TenantID: job.TenantID, CustomerID: job.CustomerID, AccountID: job.AccountID, ExportID: job.ID, EventType: "RENDER_STARTED", Result: "SUCCESS", OccurredAt: now}).Error
	})
	return claimed, err
}

func (s *GORMStore) Complete(ctx context.Context, job *projectexport.Job, workerID, fileName string, pdf []byte, hash string, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&projectexport.Job{}).Where("id=? AND version=? AND status=? AND locked_by=? AND locked_until>?", job.ID, job.Version, projectexport.StatusGenerating, workerID, now).Updates(map[string]any{"status": projectexport.StatusReady, "file_name": fileName, "file_hash": hash, "file_size": len(pdf), "file_bytes": pdf, "failure_code": "", "locked_by": "", "locked_until": nil, "completed_at": now, "updated_at": now, "version": gorm.Expr("version+1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return tx.Create(&projectexport.Event{TenantID: job.TenantID, CustomerID: job.CustomerID, AccountID: job.AccountID, ExportID: job.ID, EventType: "RENDER_SUCCEEDED", Result: "SUCCESS", OccurredAt: now}).Error
	})
}

func (s *GORMStore) Fail(ctx context.Context, job *projectexport.Job, workerID, code string, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&projectexport.Job{}).Where("id=? AND version=? AND status=? AND locked_by=? AND locked_until>?", job.ID, job.Version, projectexport.StatusGenerating, workerID, now).Updates(map[string]any{"status": projectexport.StatusFailed, "failure_code": code, "locked_by": "", "locked_until": nil, "completed_at": now, "updated_at": now, "version": gorm.Expr("version+1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return tx.Create(&projectexport.Event{TenantID: job.TenantID, CustomerID: job.CustomerID, AccountID: job.AccountID, ExportID: job.ID, EventType: "RENDER_FAILED", Result: "FAILED", ReasonCode: code, OccurredAt: now}).Error
	})
}
