package crmauth

import (
	"context"
	"errors"
	"time"

	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WithAuthorizationLock 串行在线授权复核并只写授权字段，不覆盖已轮换凭据。
// 参数：ctx为请求，hash为Cookie摘要，now和idle为本地有效性边界，update为锁内核验。
// 返回值：已提交快照；403提交清空投影，401撤销本会话，依赖失败回滚。
func (r *GORMRepository) WithAuthorizationLock(ctx context.Context, hash string, now time.Time,
	idle time.Duration, update func(context.Context, *Session) error) (*Session, error) {
	var session Session
	var rejection error
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := activeSessionQuery(tx, hash, now, idle).
			Clauses(clause.Locking{Strength: "UPDATE"}).Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		err = update(ctx, &session)
		if errors.Is(err, ErrUnauthenticated) {
			rejection = err
			return tx.Model(&Session{}).Where("session_id_hash = ?", hash).Update("revoked_at", now).Error
		}
		if err != nil && !errors.Is(err, sharedauthorization.ErrForbidden) {
			return err
		}
		rejection = err
		return tx.Model(&Session{}).Where("session_id_hash = ?", hash).Updates(map[string]any{
			"roles_json": string(session.RolesJSON), "permissions_json": string(session.PermissionsJSON),
			"data_scopes_json": string(session.DataScopesJSON), "organization_ids_json": string(session.OrganizationIDsJSON),
			"primary_org_id": session.PrimaryOrgID, "role_config_hash": session.RoleConfigHash,
			"authz_revision": session.AuthzRevision, "authorization_checked_at": session.AuthorizationCheckedAt,
			"display_name": session.DisplayName, "username": session.Username,
		}).Error
	})
	if errors.Is(err, errNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if rejection != nil {
		return nil, rejection
	}
	return &session, nil
}
