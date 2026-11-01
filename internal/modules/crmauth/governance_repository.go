package crmauth

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// AttachSessionLink 只在原登录事务仍有效且未收到票据时写入密文。
// 参数：ctx为请求，stateHash为原状态摘要，cipher为票据密文，now为服务端时钟。
// 返回值：未消费的原事务；重复桥接、过期及数据库失败返回错误。
func (r *GORMRepository) AttachSessionLink(ctx context.Context, stateHash string, cipher []byte, now time.Time) (*LoginTransaction, error) {
	var result LoginTransaction
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_hash = ? AND expires_at > ?", stateHash, now).Take(&result).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotFound
			}
			return err
		}
		if len(result.CentralTicketCipher) > 0 {
			return ErrUnauthenticated
		}
		result.CentralTicketCipher = append([]byte(nil), cipher...)
		return tx.Model(&LoginTransaction{}).Where("state_hash = ?", stateHash).Update("central_ticket_cipher", cipher).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// WithSessionLock 串行轮换服务器令牌，锁内重读防止多实例重复使用旧refresh token。
// 参数：ctx为请求，hash为Cookie摘要，now为时钟，update为持锁时的有界刷新操作。
// 返回值：最新持久会话；明确失效提交撤销后返回错误，其他失败回滚且不伪称成功。
func (r *GORMRepository) WithSessionLock(ctx context.Context, hash string, now time.Time, update sessionLockFunc) (*Session, error) {
	var result Session
	var rejection error
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now).Take(&result).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotFound
			}
			return err
		}
		changed, err := update(&result)
		if errors.Is(err, ErrUnauthenticated) || errors.Is(err, errRefreshRejected) {
			rejection = err
			return tx.Model(&Session{}).Where("session_id_hash = ?", hash).Update("revoked_at", now).Error
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return tx.Model(&Session{}).Where("session_id_hash = ?", hash).Updates(map[string]any{
			"access_token_cipher": result.AccessTokenCipher, "refresh_token_cipher": result.RefreshTokenCipher,
			"access_token_expires_at":  result.AccessTokenExpiresAt,
			"authorization_checked_at": result.AuthorizationCheckedAt,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	if rejection != nil {
		return nil, rejection
	}
	return &result, nil
}
