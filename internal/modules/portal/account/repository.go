package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository       { return &GORMRepository{db: db} }
func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	// 事务句柄通过上下文传递，保证 fn 内的后续仓储调用共享同一提交边界。
	return database.WithTransaction(ctx, r.db, fn)
}

func (r *GORMRepository) UpsertPendingLink(ctx context.Context, value *IdentityLink) (*IdentityLink, error) {
	var current IdentityLink
	err := r.tx(ctx).Where("tenant_id = ? AND platform_user_id = ?", value.TenantID, value.PlatformUserID).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if createErr := r.tx(ctx).Create(value).Error; createErr != nil {
			return nil, createErr
		}
		return value, nil
	}
	if err != nil {
		return nil, err
	}
	if current.CustomerID != value.CustomerID || current.Status == IdentityDisabled {
		return nil, ErrIdentityDisabled
	}
	if current.AccountNo != value.AccountNo {
		return nil, ErrInvalidClaims
	}
	updates := map[string]any{"contact_id": value.ContactID, "updated_by": value.UpdatedBy, "updated_at": value.UpdatedAt}
	// 补偿重试只携带不可变集成身份，不携带可变联系人信息；空展示名不能覆盖已登记的客户名称。
	if strings.TrimSpace(value.DisplayName) != "" {
		updates["display_name"] = value.DisplayName
	}
	if updateErr := r.tx(ctx).Model(&current).Updates(updates).Error; updateErr != nil {
		return nil, updateErr
	}
	return &current, nil
}

func (r *GORMRepository) FindLink(ctx context.Context, tenantID, subject string) (*IdentityLink, error) {
	var value IdentityLink
	err := r.tx(ctx).Where("tenant_id = ? AND platform_user_id = ?", tenantID, subject).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotProvisioned
	}
	return &value, err
}

type identityDisableOperation struct {
	ID                 uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	IdentityLinkID     uint64    `gorm:"column:identity_link_id;not null"`
	CustomerID         uint64    `gorm:"column:customer_id;not null"`
	ResultVersion      uint64    `gorm:"column:result_version;not null"`
	TenantID           string    `gorm:"column:tenant_id;size:64;not null;uniqueIndex:uq_portal_identity_disable_idempotency"`
	OAuthClientSubject string    `gorm:"column:oauth_client_subject;size:128;not null;uniqueIndex:uq_portal_identity_disable_idempotency"`
	IdempotencyKey     string    `gorm:"column:idempotency_key;size:128;not null;uniqueIndex:uq_portal_identity_disable_idempotency"`
	RequestHash        string    `gorm:"column:request_hash;size:64;not null"`
	PlatformUserID     string    `gorm:"column:platform_user_id;size:128;not null"`
	OccurredAt         time.Time `gorm:"column:occurred_at;precision:3;not null"`
}

func (identityDisableOperation) TableName() string { return "portal_identity_disable_operations" }

// DisableLink 在一个事务中冻结精确的客户/主体映射，并撤销该主体的全部活动会话。
// 相同业务幂等键的精确重放返回稳定结果；载荷冲突或映射属于其他客户时按拒绝处理。
func (r *GORMRepository) DisableLink(ctx context.Context, command DisableCommand, now time.Time) (DisableResult, error) {
	requestHash := disableRequestHash(command)
	requestID := strings.TrimSpace(requestctx.ID(ctx))
	if requestID == "" {
		return DisableResult{}, ErrInvalidClaims
	}
	if len(requestID) > 64 {
		sum := sha256.Sum256([]byte(requestID))
		requestID = hex.EncodeToString(sum[:])
	}
	result := DisableResult{}
	err := r.tx(ctx).Transaction(func(tx *gorm.DB) error {
		var replay identityDisableOperation
		replayErr := tx.Where("tenant_id=? AND oauth_client_subject=? AND idempotency_key=?", command.TenantID, command.ActorID, command.IdempotencyKey).Take(&replay).Error
		if replayErr == nil {
			if replay.RequestHash != requestHash || replay.CustomerID != command.CustomerID || replay.PlatformUserID != command.PlatformUserID {
				return ErrVersionConflict
			}
			result = DisableResult{CustomerID: replay.CustomerID, PlatformUserID: replay.PlatformUserID, Status: IdentityDisabled, Version: replay.ResultVersion}
			return nil
		}
		if !errors.Is(replayErr, gorm.ErrRecordNotFound) {
			return replayErr
		}
		var link IdentityLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND customer_id = ? AND platform_user_id = ?", command.TenantID, command.CustomerID, command.PlatformUserID).
			Take(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotProvisioned
			}
			return err
		}
		// 已禁用映射只能通过上方核验过的操作账本重放返回；接受新键会为已终结动作制造第二条审计历史。
		if link.Status == IdentityDisabled {
			return ErrIdentityDisabled
		}
		resultVersion := link.Version
		update := tx.Model(&IdentityLink{}).
			Where("id = ? AND tenant_id = ? AND customer_id = ? AND platform_user_id = ? AND version = ? AND status IN ?", link.ID, command.TenantID, command.CustomerID, command.PlatformUserID, link.Version, []IdentityStatus{IdentityPending, IdentityActive}).
			Updates(map[string]any{"status": IdentityDisabled, "disabled_at": now, "updated_by": command.ActorID, "updated_at": now, "version": gorm.Expr("version + 1")})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrVersionConflict
		}
		resultVersion++
		if err := tx.Model(&Session{}).
			Where("tenant_id = ? AND platform_user_id = ? AND revoked_at IS NULL", command.TenantID, command.PlatformUserID).
			Updates(map[string]any{"revoked_at": now, "updated_at": now, "updated_by": command.ActorID}).Error; err != nil {
			return err
		}
		// 管理原因只进入不可变操作账本，不复制到认证时间线；这里仅保存与请求关联的最小安全事件。
		if err := tx.Create(&AuthEvent{
			TenantID: command.TenantID, PlatformUserID: command.PlatformUserID, CustomerID: &command.CustomerID,
			Type: "PORTAL_ACCESS_DISABLED", Result: "SUCCESS", ReasonCode: "CRM_ADMINISTRATIVE_DISABLE",
			RequestID: requestID, OccurredAt: now,
		}).Error; err != nil {
			return err
		}
		operation := identityDisableOperation{TenantID: command.TenantID, OAuthClientSubject: command.ActorID, IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, IdentityLinkID: link.ID, CustomerID: command.CustomerID, PlatformUserID: command.PlatformUserID, ResultVersion: resultVersion}
		if err := tx.Table(operation.TableName()).Create(map[string]any{
			"tenant_id": operation.TenantID, "oauth_client_subject": operation.OAuthClientSubject, "idempotency_key": operation.IdempotencyKey,
			"request_hash": operation.RequestHash, "identity_link_id": operation.IdentityLinkID, "customer_id": operation.CustomerID,
			"platform_user_id": operation.PlatformUserID, "result_version": operation.ResultVersion, "occurred_at": now,
		}).Error; err != nil {
			return err
		}
		result = DisableResult{CustomerID: command.CustomerID, PlatformUserID: command.PlatformUserID, Status: IdentityDisabled, Version: resultVersion}
		return nil
	})
	return result, err
}

func (r *GORMRepository) ActivateLink(ctx context.Context, tenantID string, id, revision uint64, actor string, now time.Time) error {
	result := r.tx(ctx).Model(&IdentityLink{}).Where("tenant_id = ? AND id = ? AND status = ?", tenantID, id, IdentityPending).Updates(map[string]any{"status": IdentityActive, "activated_at": now, "last_claims_revision": revision, "last_verified_at": now, "updated_by": actor, "updated_at": now, "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotProvisioned
	}
	return nil
}

// RevertActivation 补偿远端邀请消费失败。
// revision 与激活时间共同约束更新，避免回滚覆盖后续成功登录或管理员状态变更。
func (r *GORMRepository) RevertActivation(ctx context.Context, tenantID string, id, revision uint64, actor string, activatedAt time.Time) error {
	result := r.tx(ctx).Model(&IdentityLink{}).
		Where("tenant_id = ? AND id = ? AND status = ? AND last_claims_revision = ? AND activated_at = ?", tenantID, id, IdentityActive, revision, activatedAt).
		Updates(map[string]any{"status": IdentityPending, "activated_at": nil, "last_claims_revision": 0, "last_verified_at": nil, "updated_by": actor, "updated_at": activatedAt, "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotProvisioned
	}
	return nil
}

func (r *GORMRepository) CreateActivation(ctx context.Context, value *ActivationContext) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) ConsumeActivation(ctx context.Context, stateHash string, now time.Time) (*ActivationContext, error) {
	// 行锁与 consumed_at 条件使 OAuth state 只能消费一次，并阻止并发回调同时通过。
	var value ActivationContext
	err := r.tx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("state_hash = ? AND consumed_at IS NULL AND expires_at > ?", stateHash, now).Take(&value).Error; err != nil {
			return err
		}
		result := tx.Model(&ActivationContext{}).Where("id = ? AND consumed_at IS NULL", value.ID).Update("consumed_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidLoginState
	}
	return &value, err
}

func (r *GORMRepository) CreateSession(ctx context.Context, value *Session) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindSession(ctx context.Context, tenantID, sessionHash string, now time.Time) (*Session, error) {
	// 查询直接限定租户、未撤销及双重到期时间，调用方不会拿到待二次判断的过期会话。
	var value Session
	err := r.tx(ctx).Where("tenant_id = ? AND session_id_hash = ? AND revoked_at IS NULL AND expires_at > ? AND absolute_expiry > ?", tenantID, sessionHash, now, now).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidLoginState
	}
	return &value, err
}
func (r *GORMRepository) ListSessions(ctx context.Context, tenantID, subject string, now time.Time) ([]Session, error) {
	var values []Session
	err := r.tx(ctx).
		Where("tenant_id = ? AND platform_user_id = ? AND revoked_at IS NULL AND expires_at > ? AND absolute_expiry > ?", tenantID, subject, now, now).
		Order("last_seen_at DESC, id DESC").
		Find(&values).Error
	return values, err
}
func (r *GORMRepository) FindOwnedSession(ctx context.Context, tenantID, subject, publicID string, now time.Time) (*Session, error) {
	var value Session
	err := r.tx(ctx).
		Where("tenant_id = ? AND platform_user_id = ? AND public_id = ? AND revoked_at IS NULL AND expires_at > ? AND absolute_expiry > ?", tenantID, subject, publicID, now, now).
		Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionNotFound
	}
	return &value, err
}
func (r *GORMRepository) RevokeSession(ctx context.Context, tenantID, subject, sessionHash string, now time.Time) error {
	result := r.tx(ctx).Model(&Session{}).Where("tenant_id = ? AND platform_user_id = ? AND session_id_hash = ? AND revoked_at IS NULL", tenantID, subject, sessionHash).Update("revoked_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalidLoginState
	}
	return nil
}
func (r *GORMRepository) RevokeSessionsForSubject(ctx context.Context, tenantID, subject string, now time.Time) error {
	return r.tx(ctx).Model(&Session{}).
		Where("tenant_id = ? AND platform_user_id = ? AND revoked_at IS NULL", tenantID, subject).
		Update("revoked_at", now).Error
}
func (r *GORMRepository) TouchSession(ctx context.Context, tenantID, sessionHash string, seenAt, checkedAt time.Time) error {
	updates := map[string]any{"last_seen_at": seenAt, "updated_at": seenAt}
	if !checkedAt.IsZero() {
		updates["authorization_checked_at"] = checkedAt
	}
	result := r.tx(ctx).Model(&Session{}).
		Where("tenant_id = ? AND session_id_hash = ? AND revoked_at IS NULL AND expires_at > ? AND absolute_expiry > ?", tenantID, sessionHash, seenAt, seenAt).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalidLoginState
	}
	return nil
}
func (r *GORMRepository) MarkLinkVerified(ctx context.Context, tenantID string, id, revision uint64, now time.Time) error {
	result := r.tx(ctx).Model(&IdentityLink{}).
		Where("tenant_id = ? AND id = ? AND status = ?", tenantID, id, IdentityActive).
		Updates(map[string]any{"last_claims_revision": revision, "last_verified_at": now, "updated_at": now, "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrIdentityDisabled
	}
	return nil
}
func (r *GORMRepository) WriteAuthEvent(ctx context.Context, value *AuthEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateSecurityEvent(ctx context.Context, value *SecurityEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) ListSecurityEvents(ctx context.Context, tenantID, subject string, limit int) ([]SecurityEvent, error) {
	var values []SecurityEvent
	err := r.tx(ctx).
		Where("tenant_id = ? AND platform_user_id = ?", tenantID, subject).
		Order("occurred_at DESC, id DESC").Limit(limit).
		Find(&values).Error
	return values, err
}
func (r *GORMRepository) AcknowledgeSecurityEvent(ctx context.Context, tenantID, subject, publicID string, now time.Time) error {
	var event SecurityEvent
	if err := r.tx(ctx).Where("tenant_id = ? AND platform_user_id = ? AND public_id = ?", tenantID, subject, publicID).Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSecurityEventNotFound
		}
		return err
	}
	if event.AcknowledgedAt != nil {
		return nil
	}
	result := r.tx(ctx).Model(&SecurityEvent{}).
		Where("id = ? AND tenant_id = ? AND platform_user_id = ? AND acknowledged_at IS NULL", event.ID, tenantID, subject).
		Updates(map[string]any{"acknowledged_at": now, "updated_at": now, "updated_by": subject, "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		// 已证明事件属于当前租户和主体后，并发确认导致的零更新可视为幂等成功。
		var count int64
		if err := r.tx(ctx).Model(&SecurityEvent{}).
			Where("id = ? AND tenant_id = ? AND platform_user_id = ? AND public_id = ? AND acknowledged_at IS NOT NULL", event.ID, tenantID, subject, publicID).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrSecurityEventNotFound
		}
	}
	return nil
}
