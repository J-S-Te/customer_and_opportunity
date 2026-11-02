package crmauth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errNotFound = errors.New("CRM auth record not found")

// SessionTokens 是需要原子写回的刷新结果。访问令牌与 refresh token 都是密文，
// AccessTokenExpiresAt 使用 UTC，供服务端在令牌到期前决定是否再次刷新。
type SessionTokens struct {
	AccessTokenCipher    []byte
	RefreshTokenCipher   []byte
	AccessTokenExpiresAt time.Time
}

// sessionRefreshFunc 在会话行锁内被调用：返回 changed=false 表示无需写入（例如另一个实例已刷新）。
type sessionRefreshFunc func(current *Session) (SessionTokens, bool, error)

type repository interface {
	SaveLogin(context.Context, *LoginTransaction) error
	ConsumeLogin(context.Context, string, time.Time) (*LoginTransaction, error)
	CreateSession(context.Context, *Session) error
	FindSession(context.Context, string, time.Time, time.Duration) (*Session, error)
	TouchSession(context.Context, string, time.Time, time.Time, time.Duration) error
	RecordActivity(context.Context, string, time.Time, time.Duration) (time.Time, error)
	RefreshSessionTokens(context.Context, string, time.Time, time.Duration, sessionRefreshFunc) (*Session, error)
	RevokeSession(context.Context, string, time.Time) error
	RevokeSessionsForSubject(context.Context, string, string, time.Time) error
}

// activeSessionPredicate 统一"未撤销 + 未绝对到期 + 未空闲到期"判定。迁移前的存量行
// last_interactive_at 为 NULL 时以 created_at 作为最后交互时间，升级不会立刻锁死在线会话。
const activeSessionPredicate = "session_id_hash = ? AND revoked_at IS NULL AND expires_at > ? AND COALESCE(last_interactive_at, created_at) > ?"

// activeSessionQuery 按模式限制本地会话；中央模式只查本地撤销和绝对期限。
// 参数：db为查询句柄，hash为Cookie摘要，now为时钟，idleTTL为旧模式空闲窗口。
// 返回值：带有效性条件的查询；idleTTL为零时由调用方继续检查中央权威。
func activeSessionQuery(db *gorm.DB, hash string, now time.Time, idleTTL time.Duration) *gorm.DB {
	if idleTTL <= 0 {
		return db.Where("session_id_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now)
	}
	return db.Where(activeSessionPredicate, hash, now, now.Add(-idleTTL))
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
	// 行锁与删除处于同一事务，使多个实例收到相同回调时只有一个实例能取得登录秘密。
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

// FindSession 读取未撤销且在当前模式本地期限内的会话。
// 参数：ctx为请求，hash为Cookie摘要，now为时钟，idleTTL为旧模式窗口或中央模式零值。
// 返回值：会话；失效返回未找到，数据库故障保留原始错误。
func (r *GORMRepository) FindSession(ctx context.Context, hash string, now time.Time, idleTTL time.Duration) (*Session, error) {
	var value Session
	err := activeSessionQuery(r.db.WithContext(ctx), hash, now, idleTTL).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errNotFound
	}
	return &value, err
}

// TouchSession 更新普通请求和授权核验时间，不推进真实交互窗口。
// 参数：ctx为请求，hash为Cookie摘要，now为访问时间，checkedAt为重验时间，idleTTL为本地窗口。
// 返回值：成功为空；失效返回未找到，数据库失败返回错误。
func (r *GORMRepository) TouchSession(ctx context.Context, hash string, now, checkedAt time.Time, idleTTL time.Duration) error {
	updates := map[string]any{"last_seen_at": now}
	if !checkedAt.IsZero() {
		updates["authorization_checked_at"] = checkedAt
	}
	result := activeSessionQuery(r.db.WithContext(ctx).Model(&Session{}), hash, now, idleTTL).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	// MySQL DATETIME(3) 只有毫秒精度，并行浏览器请求可能写入相同的 last_seen_at，MySQL 因值未变化
	// 报告 0 行受影响。此时重新检查权威有效条件，不能把合法并发请求误判为会话撤销，否则前端会进入 401/OAuth 刷新循环。
	var active struct {
		SessionIDHash string `gorm:"column:session_id_hash"`
	}
	err := activeSessionQuery(r.db.WithContext(ctx).Table((Session{}).TableName()), hash, now, idleTTL).
		Select("session_id_hash").Take(&active).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errNotFound
	}
	return err
}

// RecordActivity 只由显式浏览器活动接口调用：同时推进 last_seen_at 与 last_interactive_at。
// 返回权威空闲期限（最后交互 + 空闲策略与绝对到期中的较早者），供前端 schedule 下一次探测。
func (r *GORMRepository) RecordActivity(ctx context.Context, hash string, now time.Time, idleTTL time.Duration) (time.Time, error) {
	args := []any{hash, now, now.Add(-idleTTL)}
	var row struct {
		ExpiresAt time.Time
	}
	err := r.db.WithContext(ctx).Table((Session{}).TableName()).
		Select("expires_at").Where(activeSessionPredicate, args...).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, errNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	result := r.db.WithContext(ctx).Model(&Session{}).
		Where(activeSessionPredicate, args...).
		Updates(map[string]any{"last_seen_at": now, "last_interactive_at": now})
	if result.Error != nil {
		return time.Time{}, result.Error
	}
	if result.RowsAffected != 1 {
		// 同一毫秒内的并发活动上报可能写入相同 DATETIME(3) 值而报告 0 行；复核权威有效条件，
		// 合法并发不能误判为登出。
		var active struct {
			SessionIDHash string `gorm:"column:session_id_hash"`
		}
		if err := r.db.WithContext(ctx).Table((Session{}).TableName()).
			Select("session_id_hash").Where(activeSessionPredicate, args...).Take(&active).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return time.Time{}, errNotFound
			}
			return time.Time{}, err
		}
	}
	return sessionIdleDeadlineAt(now, idleTTL, row.ExpiresAt), nil
}

// RefreshSessionTokens 在会话行锁内串行化令牌刷新：只有真正需要刷新的一方调用平台令牌端点并写回，
// 其余并发请求（含其他实例）拿到已刷新的记录。这样避免多个请求同时使用一次性 refresh token
// 触发平台侧的重放保护，把整条令牌族撤销。
func (r *GORMRepository) RefreshSessionTokens(ctx context.Context, hash string, now time.Time, idleTTL time.Duration, refresh sessionRefreshFunc) (*Session, error) {
	var refreshed *Session
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(activeSessionPredicate, hash, now, now.Add(-idleTTL)).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNotFound
			}
			return err
		}
		tokens, changed, err := refresh(&row)
		if err != nil {
			return err
		}
		if changed {
			expiresAt := tokens.AccessTokenExpiresAt.UTC()
			if err := tx.Model(&Session{}).Where("session_id_hash = ?", hash).Updates(map[string]any{
				"access_token_cipher": tokens.AccessTokenCipher, "refresh_token_cipher": tokens.RefreshTokenCipher,
				"access_token_expires_at": expiresAt,
			}).Error; err != nil {
				return err
			}
			row.AccessTokenCipher, row.RefreshTokenCipher, row.AccessTokenExpiresAt = tokens.AccessTokenCipher, tokens.RefreshTokenCipher, &expiresAt
		}
		snapshot := row
		refreshed = &snapshot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refreshed, nil
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

// ApplyBackchannelLogout 在单一事务中登记防重放 JTI 并撤销目标 CRM 会话。
// 返回 replayed=true 表示该 JTI 已完整处理，调用方应按幂等成功响应。
func (r *GORMRepository) ApplyBackchannelLogout(ctx context.Context, jti, issuer, subject, sid string, expiresAt, now time.Time) (replayed bool, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entry := BackchannelLogoutReplay{JTI: jti, Issuer: issuer, ExpiresAt: expiresAt, CreatedAt: now}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			replayed = true
			return nil
		}
		query := tx.Model(&Session{}).Where("revoked_at IS NULL")
		if sid != "" {
			query = query.Where("oidc_sid = ?", sid)
		} else {
			query = query.Where("platform_user_id = ?", subject)
		}
		return query.Update("revoked_at", now).Error
	})
	return replayed, err
}
