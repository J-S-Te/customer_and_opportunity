package account

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

// WithAuthorizationLock 串行读取并提交门户授权快照，与凭据轮换事务分离。
// 参数：ctx为请求，tenant和hash限定本会话，now为有效性边界，update执行在线核验。
// 返回值：新快照；授权拒绝提交空投影，身份失效仅撤销当前行，依赖故障回滚。
func (r *GORMRepository) WithAuthorizationLock(ctx context.Context, tenant, hash string, now time.Time,
	update func(context.Context, *Session) error) (*Session, error) {
	var session Session
	var rejection error
	err := r.tx(ctx).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := activeSessionQuery(tx, tenant, hash, now, r.central).Select("*").Take(&session).Error; err != nil {
			return err
		}
		err := update(database.WithHandle(ctx, tx), &session)
		if errors.Is(err, ErrInvalidLoginState) || errors.Is(err, ErrInvalidClaims) {
			rejection = err
			return tx.Model(&Session{}).Where("id = ?", session.ID).Update("revoked_at", now).Error
		}
		if err != nil && !errors.Is(err, ErrPortalAuthorization) {
			return err
		}
		rejection = err
		roles, err := json.Marshal(session.Roles)
		if err != nil {
			return err
		}
		permissions, err := json.Marshal(session.Permissions)
		if err != nil {
			return err
		}
		scopes, err := json.Marshal(session.DataScopes)
		if err != nil {
			return err
		}
		return tx.Model(&Session{}).Where("id = ?", session.ID).Updates(map[string]any{
			"roles_json": string(roles), "permissions_json": string(permissions), "data_scopes_json": string(scopes),
			"customer_id": session.CustomerID, "authz_revision": session.AuthzRevision,
			"role_config_hash": session.RoleConfigHash, "authorization_checked_at": session.AuthorizationCheckedAt,
		}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidLoginState
	}
	if err != nil {
		return nil, ErrAuthorizationUnavailable
	}
	if rejection != nil {
		return nil, rejection
	}
	return &session, nil
}
