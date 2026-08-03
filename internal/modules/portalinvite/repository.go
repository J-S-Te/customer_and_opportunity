package portalinvite

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	LockCustomer(context.Context, string, uint64) error
	RevokePending(context.Context, string, uint64, string, string, time.Time) error
	CreateInvite(context.Context, *Invite) error
	UpsertLink(context.Context, *IdentityLink) error
	FindCurrent(context.Context, string, uint64) (*Invite, error)
	FindByInviteNo(context.Context, string, string) (*Invite, error)
	FindByTokenHash(context.Context, string) (*Invite, error)
	FindByTokenHashForUpdate(context.Context, string) (*Invite, error)
	FindIdentityLinkForInviteForUpdate(context.Context, *Invite) (*IdentityLink, error)
	ActivateIdentityLink(context.Context, *Invite, *IdentityLink, string, time.Time) error
	MarkExpired(context.Context, uint64, uint64, string, time.Time) error
	Consume(context.Context, uint64, uint64, string, string, time.Time) error
	Revoke(context.Context, uint64, uint64, string, string, time.Time) error
	CreateCompensation(context.Context, *CompensationTask) error
	FindProvisionOperation(context.Context, string, string, string) (*ProvisionOperation, error)
	FindProvisionOperationForUpdate(context.Context, string, uint64) (*ProvisionOperation, error)
	CreateProvisionOperation(context.Context, *ProvisionOperation) error
	AdvanceProvisionOperation(context.Context, *ProvisionOperation, string, map[string]any, time.Time) error
	FindInviteByID(context.Context, string, uint64) (*Invite, error)
}

// AccessDisableRepository 与邀请持久化分离，避免邀请生命周期及其测试替身隐式获得停用访问的权限。
type AccessDisableRepository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	LockCustomer(context.Context, string, uint64) error
	RevokePending(context.Context, string, uint64, string, string, time.Time) error
	FindIdentityLink(context.Context, string, uint64) (*IdentityLink, error)
	FindIdentityLinkForUpdate(context.Context, string, uint64) (*IdentityLink, error)
	FindLatestAccessDisableOperation(context.Context, string, uint64) (*AccessDisableOperation, error)
	FindAccessDisableOperation(context.Context, string, string, string) (*AccessDisableOperation, error)
	FindAccessDisableOperationForUpdate(context.Context, string, uint64) (*AccessDisableOperation, error)
	ClaimAccessDisableOperation(context.Context, *AccessDisableOperation, string, time.Time, time.Time) error
	CreateAccessDisableOperation(context.Context, *AccessDisableOperation) error
	AdvanceAccessDisableOperation(context.Context, *AccessDisableOperation, string, map[string]any, time.Time) error
	DisableIdentityLink(context.Context, *AccessDisableOperation, string, time.Time) error
}

type accessDisableFenceRepository interface {
	HasBlockingAccessDisable(context.Context, string, uint64) (bool, error)
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

func (r *GORMRepository) LockCustomer(ctx context.Context, tenant string, customerID uint64) error {
	var id uint64
	err := r.tx(ctx).Raw("SELECT id FROM crm_customers WHERE tenant_id = ? AND id = ? AND status = 'ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL FOR UPDATE", tenant, customerID).Scan(&id).Error
	if err != nil {
		return err
	}
	if id == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *GORMRepository) RevokePending(ctx context.Context, tenant string, customerID uint64, actor, reason string, now time.Time) error {
	return r.tx(ctx).Model(&Invite{}).
		Where("tenant_id = ? AND customer_id = ? AND status = ? AND deleted_at IS NULL", tenant, customerID, StatusPending).
		Updates(map[string]any{"status": StatusRevoked, "revoked_at": now, "revoked_reason": reason, "updated_by": actor, "updated_at": now, "version": gorm.Expr("version + 1")}).Error
}

func (r *GORMRepository) CreateInvite(ctx context.Context, value *Invite) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) UpsertLink(ctx context.Context, value *IdentityLink) error {
	var current IdentityLink
	err := r.tx(ctx).Where("tenant_id = ? AND deleted_at IS NULL AND (platform_user_id = ? OR (customer_id = ? AND contact_id = ?))", value.TenantID, value.PlatformUserID, value.CustomerID, value.ContactID).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.tx(ctx).Create(value).Error
	}
	if err != nil {
		return err
	}
	if current.CustomerID != value.CustomerID || current.ContactID != value.ContactID || current.PortalAccountID != value.PortalAccountID {
		return ErrVersionConflict
	}
	// DISABLED 是终态；重新开放访问必须启动新的显式业务流程，旧邀请 Saga 不能复活该映射。
	if current.Status == "DISABLED" {
		return ErrVersionConflict
	}
	result := r.tx(ctx).Model(&current).Where("version = ?", current.Version).Updates(map[string]any{
		"status": value.Status, "last_verified_at": value.LastVerifiedAt, "updated_by": value.UpdatedBy,
		"updated_at": value.UpdatedAt, "version": gorm.Expr("version + 1"),
	})
	return affected(result)
}

func (r *GORMRepository) FindCurrent(ctx context.Context, tenant string, customerID uint64) (*Invite, error) {
	var value Invite
	err := r.tx(ctx).Where("tenant_id = ? AND customer_id = ? AND deleted_at IS NULL", tenant, customerID).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "created_at"}, Desc: true}).Order("id DESC").Take(&value).Error
	return mapNotFound(&value, err)
}

func (r *GORMRepository) FindByInviteNo(ctx context.Context, tenant, inviteNo string) (*Invite, error) {
	var value Invite
	err := r.tx(ctx).Where("tenant_id = ? AND invite_no = ? AND deleted_at IS NULL", tenant, inviteNo).Take(&value).Error
	return mapNotFound(&value, err)
}

func (r *GORMRepository) FindByTokenHash(ctx context.Context, hash string) (*Invite, error) {
	var value Invite
	err := r.tx(ctx).Where("token_hash = ? AND deleted_at IS NULL", hash).Take(&value).Error
	return mapNotFound(&value, err)
}

func (r *GORMRepository) FindByTokenHashForUpdate(ctx context.Context, hash string) (*Invite, error) {
	var value Invite
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("token_hash = ? AND deleted_at IS NULL", hash).Take(&value).Error
	return mapNotFound(&value, err)
}

// FindIdentityLinkForInviteForUpdate 用邀请冻结的全部不可变身份字段定位并加锁映射；
// 仅按客户查询不够严格，旧回调绝不能激活后续替换的新映射。
func (r *GORMRepository) FindIdentityLinkForInviteForUpdate(ctx context.Context, invite *Invite) (*IdentityLink, error) {
	var value IdentityLink
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"tenant_id = ? AND customer_id = ? AND contact_id = ? AND platform_user_id = ? AND portal_account_id = ? AND deleted_at IS NULL",
		invite.TenantID, invite.CustomerID, invite.ContactID, invite.PlatformUserID, invite.PortalAccountID,
	).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVersionConflict
	}
	return &value, err
}

func (r *GORMRepository) ActivateIdentityLink(ctx context.Context, invite *Invite, link *IdentityLink, actor string, now time.Time) error {
	result := r.tx(ctx).Model(&IdentityLink{}).Where(
		"id = ? AND version = ? AND tenant_id = ? AND customer_id = ? AND contact_id = ? AND platform_user_id = ? AND portal_account_id = ? AND status = ? AND deleted_at IS NULL",
		link.ID, link.Version, invite.TenantID, invite.CustomerID, invite.ContactID, invite.PlatformUserID, invite.PortalAccountID, StatusPending,
	).Updates(map[string]any{
		"status": "ACTIVE", "last_verified_at": now, "updated_by": actor,
		"updated_at": now, "version": gorm.Expr("version + 1"),
	})
	return affected(result)
}

func mapNotFound(value *Invite, err error) (*Invite, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return value, err
}

func (r *GORMRepository) MarkExpired(ctx context.Context, id, version uint64, actor string, now time.Time) error {
	result := r.tx(ctx).Model(&Invite{}).Where("id = ? AND version = ? AND status = ? AND expires_at <= ?", id, version, StatusPending, now).
		Updates(map[string]any{"status": StatusExpired, "updated_by": actor, "updated_at": now, "version": gorm.Expr("version + 1")})
	return affected(result)
}

func (r *GORMRepository) Consume(ctx context.Context, id, version uint64, expectedSubject, actor string, now time.Time) error {
	result := r.tx(ctx).Model(&Invite{}).Where("id = ? AND version = ? AND status = ? AND platform_user_id = ? AND expires_at > ?", id, version, StatusPending, expectedSubject, now).
		Updates(map[string]any{"status": StatusUsed, "used_at": now, "updated_by": actor, "updated_at": now, "version": gorm.Expr("version + 1")})
	return affected(result)
}

func (r *GORMRepository) Revoke(ctx context.Context, id, version uint64, actor, reason string, now time.Time) error {
	result := r.tx(ctx).Model(&Invite{}).Where("id = ? AND version = ? AND status = ?", id, version, StatusPending).
		Updates(map[string]any{"status": StatusRevoked, "revoked_at": now, "revoked_reason": reason, "updated_by": actor, "updated_at": now, "version": gorm.Expr("version + 1")})
	return affected(result)
}

func affected(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (r *GORMRepository) CreateCompensation(ctx context.Context, task *CompensationTask) error {
	return r.tx(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "task_no"}}, DoNothing: true}).Create(task).Error
}

func (r *GORMRepository) FindProvisionOperation(ctx context.Context, tenant, actor, key string) (*ProvisionOperation, error) {
	var value ProvisionOperation
	err := r.tx(ctx).Where("tenant_id=? AND actor_id=? AND idempotency_key=? AND deleted_at IS NULL", tenant, actor, key).Take(&value).Error
	return &value, err
}

func (r *GORMRepository) FindProvisionOperationForUpdate(ctx context.Context, tenant string, id uint64) (*ProvisionOperation, error) {
	var value ProvisionOperation
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=? AND deleted_at IS NULL", tenant, id).Take(&value).Error
	return &value, err
}

func (r *GORMRepository) CreateProvisionOperation(ctx context.Context, value *ProvisionOperation) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) AdvanceProvisionOperation(ctx context.Context, value *ProvisionOperation, expectedStage string, updates map[string]any, now time.Time) error {
	updates["updated_at"] = now
	updates["updated_by"] = value.ActorID
	updates["version"] = gorm.Expr("version+1")
	result := r.tx(ctx).Model(&ProvisionOperation{}).
		Where("id=? AND tenant_id=? AND version=? AND stage=? AND deleted_at IS NULL", value.ID, value.TenantID, value.Version, expectedStage).
		Updates(updates)
	if err := affected(result); err != nil {
		return err
	}
	value.Version++
	value.UpdatedAt = now
	if stage, ok := updates["stage"].(string); ok {
		value.Stage = stage
	}
	if status, ok := updates["status"].(string); ok {
		value.Status = status
	}
	if platformUserID, ok := updates["platform_user_id"].(string); ok {
		value.PlatformUserID = platformUserID
	}
	if accountNo, ok := updates["account_no"].(string); ok {
		value.AccountNo = accountNo
	}
	if portalAccountID, ok := updates["portal_account_id"].(string); ok {
		value.PortalAccountID = portalAccountID
	}
	return nil
}

func (r *GORMRepository) FindInviteByID(ctx context.Context, tenant string, id uint64) (*Invite, error) {
	var value Invite
	err := r.tx(ctx).Where("tenant_id=? AND id=? AND deleted_at IS NULL", tenant, id).Take(&value).Error
	return mapNotFound(&value, err)
}

func (r *GORMRepository) FindIdentityLink(ctx context.Context, tenant string, customerID uint64) (*IdentityLink, error) {
	values := make([]IdentityLink, 0, 2)
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND status IN ? AND deleted_at IS NULL", tenant, customerID, []string{StatusPending, StatusUsed, "ACTIVE"}).Order("id DESC").Limit(2).Find(&values).Error
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrNotFound
	}
	if len(values) != 1 {
		return nil, ErrVersionConflict
	}
	return &values[0], nil
}

func (r *GORMRepository) FindIdentityLinkForUpdate(ctx context.Context, tenant string, customerID uint64) (*IdentityLink, error) {
	values := make([]IdentityLink, 0, 2)
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND customer_id=? AND status IN ? AND deleted_at IS NULL", tenant, customerID, []string{StatusPending, StatusUsed, "ACTIVE"}).
		Order("id DESC").Limit(2).Find(&values).Error
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrNotFound
	}
	if len(values) != 1 {
		return nil, ErrVersionConflict
	}
	return &values[0], nil
}

func (r *GORMRepository) FindAccessDisableOperation(ctx context.Context, tenant, actor, key string) (*AccessDisableOperation, error) {
	var value AccessDisableOperation
	err := r.tx(ctx).Where("tenant_id=? AND actor_id=? AND idempotency_key=? AND deleted_at IS NULL", tenant, actor, key).Take(&value).Error
	return &value, err
}

func (r *GORMRepository) FindAccessDisableOperationForUpdate(ctx context.Context, tenant string, id uint64) (*AccessDisableOperation, error) {
	var value AccessDisableOperation
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=? AND deleted_at IS NULL", tenant, id).Take(&value).Error
	return &value, err
}

// API 重试执行远程步骤前必须领取有限期租约；后台任务也通过相同租约字段和 SKIP LOCKED 批量领取，
// 两条执行路径不会主动并发派发同一停用操作。
func (r *GORMRepository) ClaimAccessDisableOperation(ctx context.Context, value *AccessDisableOperation, owner string, now, until time.Time) error {
	result := r.tx(ctx).Model(&AccessDisableOperation{}).
		Where(`id=? AND tenant_id=? AND version=? AND deleted_at IS NULL AND stage IN (?,?) AND (
            (status=? AND next_retry_at<=?) OR
            (status=? AND (locked_until IS NULL OR locked_until<=?))
        )`, value.ID, value.TenantID, value.Version, DisableStagePrepared, DisableStageMappingDisabled,
			DisableStatusRetryWait, now, DisableStatusProcessing, now).
		Updates(map[string]any{
			"status": DisableStatusProcessing, "locked_by": owner, "locked_until": until,
			"updated_at": now, "updated_by": value.ActorID, "version": gorm.Expr("version+1"),
		})
	if err := affected(result); err != nil {
		return err
	}
	value.Status, value.LockedBy, value.LockedUntil = DisableStatusProcessing, owner, &until
	value.UpdatedAt, value.Version = now, value.Version+1
	return nil
}

func (r *GORMRepository) FindLatestAccessDisableOperation(ctx context.Context, tenant string, customerID uint64) (*AccessDisableOperation, error) {
	var value AccessDisableOperation
	err := r.tx(ctx).Where("tenant_id=? AND customer_id=? AND deleted_at IS NULL", tenant, customerID).
		Order("created_at DESC").Order("id DESC").Take(&value).Error
	return &value, err
}

func (r *GORMRepository) HasBlockingAccessDisable(ctx context.Context, tenant string, customerID uint64) (bool, error) {
	var count int64
	err := r.tx(ctx).Model(&AccessDisableOperation{}).
		Where("tenant_id=? AND customer_id=? AND status IN ? AND deleted_at IS NULL", tenant, customerID, []string{DisableStatusProcessing, DisableStatusRetryWait, DisableStatusDeadLetter}).
		Count(&count).Error
	return count > 0, err
}

func (r *GORMRepository) CreateAccessDisableOperation(ctx context.Context, value *AccessDisableOperation) error {
	return r.tx(ctx).Create(value).Error
}

func (r *GORMRepository) AdvanceAccessDisableOperation(ctx context.Context, value *AccessDisableOperation, expectedStage string, updates map[string]any, now time.Time) error {
	updates["updated_at"] = now
	updates["updated_by"] = value.ActorID
	updates["version"] = gorm.Expr("version+1")
	result := r.tx(ctx).Model(&AccessDisableOperation{}).
		Where("id=? AND tenant_id=? AND version=? AND stage=? AND deleted_at IS NULL", value.ID, value.TenantID, value.Version, expectedStage).
		Updates(updates)
	if err := affected(result); err != nil {
		return err
	}
	value.Version++
	value.UpdatedAt = now
	if stage, ok := updates["stage"].(string); ok {
		value.Stage = stage
	}
	if status, ok := updates["status"].(string); ok {
		value.Status = status
	}
	if completedAt, ok := updates["completed_at"].(time.Time); ok {
		value.CompletedAt = &completedAt
	}
	return nil
}

func (r *GORMRepository) DisableIdentityLink(ctx context.Context, operation *AccessDisableOperation, actor string, now time.Time) error {
	var link IdentityLink
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND tenant_id=? AND customer_id=? AND contact_id=? AND platform_user_id=? AND portal_account_id=? AND deleted_at IS NULL",
			operation.IdentityLinkID, operation.TenantID, operation.CustomerID, operation.ContactID, operation.PlatformUserID, operation.PortalAccountID).
		Take(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrVersionConflict
	}
	if err != nil {
		return err
	}
	if link.Status == "DISABLED" {
		return nil
	}
	if link.Version != operation.IdentityLinkVersion {
		return ErrVersionConflict
	}
	if link.Status != StatusPending && link.Status != StatusUsed && link.Status != "ACTIVE" {
		return ErrVersionConflict
	}
	result := r.tx(ctx).Model(&IdentityLink{}).
		Where("id=? AND tenant_id=? AND version=? AND status=?", link.ID, operation.TenantID, link.Version, link.Status).
		Updates(map[string]any{"status": "DISABLED", "updated_by": actor, "updated_at": now, "version": gorm.Expr("version+1")})
	return affected(result)
}
