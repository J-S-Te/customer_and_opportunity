package account

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

type centralSessionUpdate func(*Session) (bool, error)
type centralRepository interface {
	AttachActivationTicket(context.Context, string, []byte, time.Time) (*ActivationContext, error)
	WithCentralSessionLock(context.Context, string, string, time.Time, centralSessionUpdate) (*Session, error)
}

// AcceptSessionLink 保存同浏览器票据后继续原nonce/PKCE的门户OIDC认证。
// 参数：ctx为请求，state为HttpOnly Cookie已校验的原始状态，ticket为平台短效票据。
// 返回值：原始提供方授权地址；过期、重复桥接或依赖故障时返回明确错误。
func (s *Service) AcceptSessionLink(ctx context.Context, state, ticket string) (string, error) {
	if s.centralClient == nil || state == "" || ticket == "" || len(ticket) > 128 {
		return "", ErrInvalidLoginState
	}
	repo, ok := s.repo.(centralRepository)
	if !ok {
		return "", ErrAuthorizationUnavailable
	}
	cipher, err := s.protector.Encrypt(ctx, []byte(ticket))
	if err != nil {
		return "", ErrAuthorizationUnavailable
	}
	activation, err := repo.AttachActivationTicket(ctx, hash(state), cipher, s.clock.Now().UTC())
	if err != nil {
		return "", err
	}
	nonce, err := s.protector.Decrypt(ctx, activation.NonceCipher)
	if err != nil {
		return "", ErrInvalidLoginState
	}
	verifier, err := s.protector.Decrypt(ctx, activation.PKCEVerifierCipher)
	if err != nil {
		return "", ErrInvalidLoginState
	}
	challenge := sha256.Sum256(verifier)
	return s.oidc.AuthorizationURL(state, string(nonce), base64.RawURLEncoding.EncodeToString(challenge[:]), activation.ReturnPath)
}

// readCentral 保留门户独立audience、客户绑定和权限，只消费平台生命周期。
// 参数：ctx为请求，session为门户密文会话，interact表示通过同源保护的真实活动。
// 返回值：非敏感中央窗口；明确失效401，暂时故障503。
func (s *Service) readCentral(ctx context.Context, session *Session, interact bool) (central.Context, error) {
	if len(session.CentralBindingCipher) == 0 {
		return central.Context{}, ErrInvalidLoginState
	}
	secret, err := s.protector.Decrypt(ctx, session.CentralBindingCipher)
	if err != nil || len(secret) == 0 {
		return central.Context{}, ErrInvalidLoginState
	}
	var state central.Context
	if interact {
		state, err = s.centralClient.Activity(ctx, string(secret))
	} else {
		state, err = s.centralClient.State(ctx, string(secret))
	}
	if err == nil {
		err = central.Validate(state, s.centralExpectation, session.PlatformUserID)
	}
	if errors.Is(err, central.ErrInactive) || errors.Is(err, central.ErrBindingConflict) {
		return central.Context{}, ErrInvalidLoginState
	}
	if err != nil {
		return central.Context{}, ErrAuthorizationUnavailable
	}
	if state.SessionID != session.CentralSessionID {
		return central.Context{}, ErrInvalidLoginState
	}
	return state, nil
}

// Lifecycle 为已认证门户状态及活动端点读取精确中央关联。
// 参数：ctx为请求，tenant为服务配置租户，token为HttpOnly Cookie，interact为显式活动。
// 返回值：生命周期；客户及角色校验由原门户认证中间件先执行。
func (s *Service) Lifecycle(ctx context.Context, tenant, token string, interact bool) (central.Context, error) {
	if s.centralClient == nil {
		return central.Context{}, ErrAuthorizationUnavailable
	}
	if token == "" {
		return central.Context{}, ErrInvalidLoginState
	}
	session, err := s.repo.FindSession(ctx, tenant, hash(token), s.clock.Now().UTC())
	if err != nil {
		if s.dependencyUnavailable(err) {
			return central.Context{}, ErrAuthorizationUnavailable
		}
		return central.Context{}, ErrInvalidLoginState
	}
	return s.readCentral(ctx, session, interact)
}

// refreshCentralAccess 在行锁内重读并轮换，保持原始绝对截止和客户快照不变。
// 参数：ctx为请求，session为已通过中央有效性检查的门户会话。
// 返回值：最新持久记录；明确拒绝仅终止本会话，临时失败不注销用户。
func (s *Service) refreshCentralAccess(ctx context.Context, session *Session) (*Session, error) {
	if session.AccessTokenExpiresAt.After(s.clock.Now().UTC().Add(time.Minute)) {
		return session, nil
	}
	repo, ok := s.repo.(centralRepository)
	if !ok {
		return nil, ErrAuthorizationUnavailable
	}
	client, ok := s.oidc.(CentralOIDCClient)
	if !ok {
		return nil, ErrAuthorizationUnavailable
	}
	rotationCtx, cancel := central.RotationContext(ctx)
	defer cancel()
	ctx = rotationCtx
	return repo.WithCentralSessionLock(ctx, session.TenantID, session.SessionIDHash, s.clock.Now().UTC(), func(current *Session) (bool, error) {
		if current.AccessTokenExpiresAt.After(s.clock.Now().UTC().Add(time.Minute)) {
			return false, nil
		}
		if _, err := s.readCentral(ctx, current, false); err != nil {
			return false, err
		}
		refresh, err := s.protector.Decrypt(ctx, current.RefreshTokenCipher)
		if err != nil || len(refresh) == 0 {
			return false, ErrInvalidLoginState
		}
		tokens, err := client.RefreshToken(ctx, string(refresh))
		if err != nil {
			return false, err
		}
		if tokens.AccessToken == "" || tokens.RefreshToken == "" || !tokens.ExpiresAt.After(s.clock.Now().UTC()) {
			return false, ErrAuthorizationUnavailable
		}
		current.AccessTokenCipher, err = s.protector.Encrypt(ctx, []byte(tokens.AccessToken))
		if err != nil {
			return false, ErrAuthorizationUnavailable
		}
		current.RefreshTokenCipher, err = s.protector.Encrypt(ctx, []byte(tokens.RefreshToken))
		if err != nil {
			return false, ErrAuthorizationUnavailable
		}
		current.AccessTokenExpiresAt = tokens.ExpiresAt.UTC()
		// 先提交轮换材料，再由原业务请求重新核验权限；故障时不得沿用旧缓存。
		current.AuthorizationCheckedAt = time.Unix(1, 0).UTC()
		return true, nil
	})
}

// dependencyUnavailable 区分在线身份确定失效与原请求取消、数据库等暂时依赖故障。
// 参数：err为认证调用链返回的原错误。
// 返回值：中央模式下未知依赖故障为true；已知失效保持原有拒绝语义。
func (s *Service) dependencyUnavailable(err error) bool {
	return s.centralClient != nil && err != nil &&
		!errors.Is(err, ErrInvalidLoginState) && !errors.Is(err, ErrSessionNotFound) &&
		!errors.Is(err, ErrIdentityDisabled) && !errors.Is(err, ErrNotProvisioned)
}
