package crmauth

import (
	"context"
	"errors"
	"time"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

// tokenRefreshResult 是访问及刷新令牌的服务器端轮换结果，不能进入浏览器响应。
type tokenRefreshResult struct {
	AccessToken, RefreshToken string
	ExpiresAt                 time.Time
}

// tokenRefresher 是仅在中央有效且持有数据库会话锁时可使用的刷新能力。
type tokenRefresher interface {
	RefreshToken(context.Context, string) (tokenRefreshResult, error)
}
type centralExchanger interface {
	ExchangeWithCentral(context.Context, string, string, string, string) (verifiedClaims, error)
}
type sessionLockFunc func(*Session) (bool, error)
type governanceRepository interface {
	AttachSessionLink(context.Context, string, []byte, time.Time) (*LoginTransaction, error)
	WithSessionLock(context.Context, string, time.Time, sessionLockFunc) (*Session, error)
}

// AcceptSessionLink 将票据保存到同一浏览器未消费的原始登录事务，再继续标准OIDC。
// 参数：ctx为请求，state为已由HttpOnly Cookie核对的原始状态，ticket为平台随机票据。
// 返回值：原nonce和PKCE约束的提供方地址；重放、过期或存储失败时返回错误。
func (s *Service) AcceptSessionLink(ctx context.Context, state, ticket string) (LoginStart, error) {
	if s.options.CentralClient == nil || state == "" || ticket == "" || len(ticket) > 128 {
		return LoginStart{}, ErrUnauthenticated
	}
	cipher, err := s.codec.encrypt(ticket)
	if err != nil {
		return LoginStart{}, err
	}
	repo, ok := s.repo.(governanceRepository)
	if !ok {
		return LoginStart{}, ErrAuthorizationUnavailable
	}
	transaction, err := repo.AttachSessionLink(ctx, tokenHash(state), cipher, s.now().UTC())
	if err != nil {
		return LoginStart{}, err
	}
	nonce, err := s.codec.decrypt(transaction.NonceCipher)
	if err != nil {
		return LoginStart{}, err
	}
	verifier, err := s.codec.decrypt(transaction.CodeVerifierCipher)
	if err != nil {
		return LoginStart{}, err
	}
	if forced, ok := s.oidc.(forcedLoginOIDCClient); ok && transaction.ForceLogin {
		return LoginStart{AuthorizationURL: forced.AuthorizationURLWithPrompt(state, nonce, verifier, true), State: state}, nil
	}
	return LoginStart{AuthorizationURL: s.oidc.AuthorizationURL(state, nonce, verifier), State: state}, nil
}

// centralState 只消费平台权威期限，不使用本地last_seen或刷新时间推算空闲窗口。
// 参数：ctx为请求，session为已认证本地会话，interact为已通过同源保护的真实活动。
// 返回值：权威生命周期；失效仅撤销当前会话，临时故障保留记录并返回依赖错误。
func (s *Service) centralState(ctx context.Context, session *Session, interact bool) (central.Context, error) {
	if s.options.CentralClient == nil {
		return central.Context{}, ErrAuthorizationUnavailable
	}
	if len(session.CentralBindingCipher) == 0 {
		return central.Context{}, ErrUnauthenticated
	}
	token, err := s.codec.decrypt(session.CentralBindingCipher)
	if err != nil {
		return central.Context{}, ErrUnauthenticated
	}
	var state central.Context
	if interact {
		state, err = s.options.CentralClient.Activity(ctx, token)
	} else {
		state, err = s.options.CentralClient.State(ctx, token)
	}
	if err == nil {
		err = central.Validate(state, s.options.CentralExpectation, session.PlatformUserID)
	}
	if errors.Is(err, central.ErrInactive) || errors.Is(err, central.ErrBindingConflict) {
		if revokeErr := s.repo.RevokeSession(ctx, session.SessionIDHash, s.now().UTC()); revokeErr != nil {
			return central.Context{}, ErrAuthorizationUnavailable
		}
		return central.Context{}, ErrUnauthenticated
	}
	if err != nil {
		return central.Context{}, ErrAuthorizationUnavailable
	}
	if state.SessionID != session.CentralSessionID {
		return central.Context{}, ErrUnauthenticated
	}
	return state, nil
}

// Lifecycle 为同源状态与活动端点提供中央非敏感投影。
// 参数：ctx为请求，rawSession为当前HttpOnly Cookie，interact表示显式交互。
// 返回值：权威窗口；无Cookie或旧未绑定会话返回未认证。
func (s *Service) Lifecycle(ctx context.Context, rawSession string, interact bool) (central.Context, error) {
	if rawSession == "" {
		return central.Context{}, ErrUnauthenticated
	}
	session, err := s.repo.FindSession(ctx, tokenHash(rawSession), s.now().UTC())
	if errors.Is(err, errNotFound) {
		return central.Context{}, ErrUnauthenticated
	}
	if err != nil {
		return central.Context{}, ErrAuthorizationUnavailable
	}
	if session.TenantID != s.options.TenantID {
		return central.Context{}, ErrUnauthenticated
	}
	return s.centralState(ctx, session, interact)
}

// refreshSessionAccess 在真实数据库行锁内重读并轮换，普通请求不覆盖新令牌。
// 参数：ctx为请求，session为本次读取的本地会话；中央有效性已在调用前校验。
// 返回值：最新持久会话；明确拒绝要求重登，网络或存储故障保留会话。
func (s *Service) refreshSessionAccess(ctx context.Context, session *Session) (*Session, error) {
	now := s.now().UTC()
	if session.AccessTokenExpiresAt.After(now.Add(time.Minute)) {
		return session, nil
	}
	repo, ok := s.repo.(governanceRepository)
	if !ok {
		return nil, ErrAuthorizationUnavailable
	}
	refresher, ok := s.oidc.(tokenRefresher)
	if !ok {
		return nil, ErrAuthorizationUnavailable
	}
	rotationCtx, cancel := central.RotationContext(ctx)
	defer cancel()
	ctx = rotationCtx
	updated, err := repo.WithSessionLock(ctx, session.SessionIDHash, now, func(current *Session) (bool, error) {
		if current.AccessTokenExpiresAt.After(s.now().UTC().Add(time.Minute)) {
			return false, nil
		}
		// 锁内再次读权威状态，等待期间到期的中央周期不得刷新。
		binding, decryptErr := s.codec.decrypt(current.CentralBindingCipher)
		if decryptErr != nil {
			return false, ErrUnauthenticated
		}
		state, stateErr := s.options.CentralClient.State(ctx, binding)
		if errors.Is(stateErr, central.ErrInactive) {
			return false, ErrUnauthenticated
		}
		if stateErr != nil {
			return false, ErrAuthorizationUnavailable
		}
		if central.Validate(state, s.options.CentralExpectation, current.PlatformUserID) != nil || state.SessionID != current.CentralSessionID {
			return false, ErrUnauthenticated
		}
		refresh, decryptErr := s.codec.decrypt(current.RefreshTokenCipher)
		if decryptErr != nil || refresh == "" {
			return false, ErrUnauthenticated
		}
		tokens, refreshErr := refresher.RefreshToken(ctx, refresh)
		if refreshErr != nil {
			return false, refreshErr
		}
		if tokens.AccessToken == "" || tokens.RefreshToken == "" || !tokens.ExpiresAt.After(s.now().UTC()) {
			return false, ErrAuthorizationUnavailable
		}
		current.AccessTokenCipher, decryptErr = s.codec.encrypt(tokens.AccessToken)
		if decryptErr != nil {
			return false, decryptErr
		}
		current.RefreshTokenCipher, decryptErr = s.codec.encrypt(tokens.RefreshToken)
		if decryptErr != nil {
			return false, decryptErr
		}
		current.AccessTokenExpiresAt = tokens.ExpiresAt.UTC()
		// 先提交轮换材料，再由原业务请求重新核验权限；故障时不得沿用旧缓存。
		current.AuthorizationCheckedAt = time.Unix(1, 0).UTC()
		return true, nil
	})
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, errRefreshRejected) || errors.Is(err, errNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, ErrAuthorizationUnavailable
	}
	return updated, nil
}

// unavailablePrincipal 保留数据库故障语义，避免业务中间件误判为用户掉线。
// 参数：err为本地数据库错误。
// 返回值：空主体及失效或依赖错误。
func unavailablePrincipal(err error) (sharedauth.Principal, error) {
	if errors.Is(err, errNotFound) {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	return sharedauth.Principal{}, ErrAuthorizationUnavailable
}
