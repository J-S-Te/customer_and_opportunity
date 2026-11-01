package account

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// EnableCentralSessions 由组合根为本实例启用中央唯一空闲判定。
// 参数：无。
// 返回值：无；后续查询仍校验本地撤销和原始绝对截止。
func (r *GORMRepository) EnableCentralSessions() { r.central = true }

// AttachActivationTicket 在原激活事务的行锁内附加票据，不消费邀请或修改客户边界。
// 参数：ctx为请求，state为原状态摘要，cipher为平台票据密文，now为时钟。
// 返回值：原始激活事务；到期、已消费或重复票据返回未认证。
func (r *GORMRepository) AttachActivationTicket(ctx context.Context, state string, cipher []byte, now time.Time) (*ActivationContext, error) {
	var result ActivationContext
	err := r.tx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_hash = ? AND consumed_at IS NULL AND expires_at > ?", state, now).Take(&result).Error; err != nil {
			return err
		}
		if len(result.CentralTicketCipher) > 0 {
			return ErrInvalidLoginState
		}
		return tx.Model(&ActivationContext{}).Where("id = ?", result.ID).Update("central_ticket_cipher", cipher).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrInvalidLoginState) {
		return nil, ErrInvalidLoginState
	}
	if err != nil {
		return nil, ErrAuthorizationUnavailable
	}
	result.CentralTicketCipher = cipher
	return &result, nil
}

// WithCentralSessionLock 在数据库行锁内轮换服务器令牌，普通last_seen更新不会覆盖新材料。
// 参数：ctx为请求，tenant为可信租户，hash为Cookie摘要，now为时钟，update为有界刷新操作。
// 返回值：最新持久会话；确定失效提交当前行撤销，其他失败保持依赖错误。
func (r *GORMRepository) WithCentralSessionLock(ctx context.Context, tenant, hash string, now time.Time, update centralSessionUpdate) (*Session, error) {
	var session Session
	var rejected error
	err := r.tx(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND session_id_hash = ? AND revoked_at IS NULL AND expires_at > ? AND absolute_expiry > ?", tenant, hash, now, now).Take(&session).Error; err != nil {
			return err
		}
		changed, err := update(&session)
		if errors.Is(err, ErrInvalidLoginState) || errors.Is(err, ErrInvalidClaims) {
			rejected = ErrInvalidLoginState
			return tx.Model(&Session{}).Where("id = ?", session.ID).Update("revoked_at", now).Error
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return tx.Model(&Session{}).Where("id = ?", session.ID).Updates(map[string]any{
			"access_token_cipher": session.AccessTokenCipher, "refresh_token_cipher": session.RefreshTokenCipher,
			"access_token_expires_at":  session.AccessTokenExpiresAt,
			"authorization_checked_at": session.AuthorizationCheckedAt,
		}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) || rejected != nil {
		return nil, ErrInvalidLoginState
	}
	if err != nil {
		return nil, ErrAuthorizationUnavailable
	}
	return &session, nil
}
