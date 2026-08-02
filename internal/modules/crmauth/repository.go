package crmauth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errNotFound = errors.New("CRM auth record not found")

type repository interface {
	SaveLogin(context.Context, *LoginTransaction) error
	ConsumeLogin(context.Context, string, time.Time) (*LoginTransaction, error)
	CreateSession(context.Context, *Session) error
	FindSession(context.Context, string, time.Time) (*Session, error)
	TouchSession(context.Context, string, time.Time, time.Time) error
	RevokeSession(context.Context, string, time.Time) error
	RevokeSessionsForSubject(context.Context, string, string, time.Time) error
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (r *GORMRepository) SaveLogin(ctx context.Context, value *LoginTransaction) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "state_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"tenant_id", "nonce_cipher", "code_verifier_cipher", "return_path", "expires_at", "created_at"}),
	}).Create(value).Error
}

func (r *GORMRepository) ConsumeLogin(ctx context.Context, stateHash string, now time.Time) (*LoginTransaction, error) {
	var value LoginTransaction
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("state_hash = ? AND expires_at > ?", stateHash, now).Take(&value).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return tx.Where("state_hash = ?", stateHash).Delete(&LoginTransaction{}).Error
	})
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (r *GORMRepository) CreateSession(ctx context.Context, value *Session) error {
	return r.db.WithContext(ctx).Create(value).Error
}

func (r *GORMRepository) FindSession(ctx context.Context, hash string, now time.Time) (*Session, error) {
	var value Session
	err := r.db.WithContext(ctx).
		Where("session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	return &value, err
}

func (r *GORMRepository) TouchSession(ctx context.Context, hash string, now, checkedAt time.Time) error {
	updates := map[string]any{"last_seen_at": now}
	if !checkedAt.IsZero() {
		updates["authorization_checked_at"] = checkedAt
	}
	result := r.db.WithContext(ctx).Model(&Session{}).
		Where("session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	// MySQL DATETIME(3) stores timestamps at millisecond precision. Parallel browser requests may
	// therefore write the same last_seen_at (and authorization_checked_at) value, in which case
	// MySQL reports zero changed rows even though the session is still active. Recheck the
	// authoritative predicates before treating this as a missing/revoked session; otherwise one
	// request succeeds while its siblings return 401 and the frontend starts an OAuth refresh loop.
	var active struct {
		SessionIDHash string `gorm:"column:session_id_hash"`
	}
	err := r.db.WithContext(ctx).Table((Session{}).TableName()).
		Select("session_id_hash").
		Where("session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now).
		Take(&active).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errNotFound
	}
	return err
}

func (r *GORMRepository) RevokeSession(ctx context.Context, hash string, now time.Time) error {
	return r.db.WithContext(ctx).Model(&Session{}).
		Where("session_id_hash = ? AND revoked_at IS NULL", hash).Update("revoked_at", now).Error
}

func (r *GORMRepository) RevokeSessionsForSubject(ctx context.Context, tenantID, subject string, now time.Time) error {
	return r.db.WithContext(ctx).Model(&Session{}).
		Where("tenant_id = ? AND platform_user_id = ? AND revoked_at IS NULL", tenantID, subject).
		Update("revoked_at", now).Error
}
