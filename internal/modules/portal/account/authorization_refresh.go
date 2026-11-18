package account

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

type authorizationRepository interface {
	WithAuthorizationLock(context.Context, string, string, time.Time,
		func(context.Context, *Session) error) (*Session, error)
}

// revalidatePortalAuthorization 在轮换提交后独立串行读取门户授权。
// 参数：ctx为请求，session为当前会话，数据库锁内重读确保并发请求不回填旧权限。
// 返回值：原Cookie对应的新快照，403清空权限，401仅撤销本会话，503保留会话。
func (s *Service) revalidatePortalAuthorization(ctx context.Context, session *Session) (*Session, error) {
	repo, ok := s.repo.(authorizationRepository)
	if !ok {
		return nil, ErrAuthorizationUnavailable
	}
	updated, err := repo.WithAuthorizationLock(ctx, session.TenantID, session.SessionIDHash,
		s.clock.Now().UTC(), func(lockedCtx context.Context, current *Session) error {
			now := s.clock.Now().UTC()
			if !current.ExpiresAt.After(now) || !current.AbsoluteExpiry.After(now) {
				return ErrInvalidLoginState
			}
			if s.centralClient != nil {
				if _, err := s.readCentral(lockedCtx, current, false); err != nil {
					return err
				}
			} else if !current.LastSeenAt.Add(portalSessionIdleTimeout).After(now) {
				return ErrInvalidLoginState
			}
			if !authorizationrefresh.Forced(ctx) && now.Sub(current.AuthorizationCheckedAt) < authorizationCheckInterval {
				return nil
			}
			return s.replacePortalAuthorization(lockedCtx, current, now)
		})
	if err != nil && !errors.Is(err, ErrInvalidLoginState) && !errors.Is(err, ErrInvalidClaims) &&
		!errors.Is(err, ErrPortalAuthorization) {
		return nil, ErrAuthorizationUnavailable
	}
	return updated, err
}

// replacePortalAuthorization 校验可信身份和当前客户边界后替换门户授权。
// 参数：ctx为持锁事务上下文，session为最新会话，now为锁内核验时间。
// 返回值：成功为空，身份错位返回401，无有效门户授权返回403，依赖异常503。
func (s *Service) replacePortalAuthorization(ctx context.Context, session *Session, now time.Time) error {
	access, err := s.protector.Decrypt(ctx, session.AccessTokenCipher)
	if err != nil {
		return ErrInvalidLoginState
	}
	current, err := s.oidc.UserInfo(ctx, string(access))
	if errors.Is(err, sharedauthorization.ErrForbidden) {
		clearPortalAuthorization(session)
		return ErrPortalAuthorization
	}
	if errors.Is(err, sharedauthorization.ErrTokenRejected) {
		return ErrInvalidLoginState
	}
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	if current.IdentityID != session.PlatformUserID || current.TenantID != session.TenantID ||
		current.RoleConfigHash != s.roleConfigHash || current.AuthzRevision == 0 {
		return ErrInvalidClaims
	}
	if !validPortalAuthorization(current, s.environmentCode) {
		clearPortalAuthorization(session)
		return ErrPortalAuthorization
	}
	candidate := *session
	candidate.Roles, candidate.Permissions = append([]string(nil), current.Roles...), append([]string(nil), current.Permissions...)
	candidate.DataScopes = cloneDataScopes(current.DataScopes)
	if err := s.validatePortalSessionBoundary(ctx, &candidate, &current); err != nil {
		if errors.Is(err, ErrAuthorizationUnavailable) {
			return err
		}
		return ErrInvalidLoginState
	}
	candidate.AuthzRevision, candidate.AuthorizationCheckedAt = current.AuthzRevision, now
	candidate.RoleConfigHash = current.RoleConfigHash
	*session = candidate
	return nil
}

// validatePortalSessionBoundary 根据当前角色重新核验客户来源及绑定，不沿用旧宽范围。
// 参数：ctx为请求，session为新授权候选或已缓存快照，current为本次在线声明或空。
// 返回值：成功为空；客户错位拒绝，依赖故障保留503语义。
func (s *Service) validatePortalSessionBoundary(ctx context.Context, session *Session, current *Claims) error {
	if isPortalSuperAdmin(session.Roles) {
		session.CustomerID = 0
		return nil
	}
	if (s.usePlatformBinding || s.localAuthority != nil) && current != nil {
		customerID, err := s.resolveCustomerReference(ctx, session.TenantID, session.PlatformUserID, current.CustomerRef, false)
		if err != nil {
			if s.authorizationDependencyUnavailable(ctx, err) {
				return ErrAuthorizationUnavailable
			}
			return ErrIdentityDisabled
		}
		if session.CustomerID != 0 && session.CustomerID != customerID {
			return ErrIdentityDisabled
		}
		session.CustomerID = customerID
	}
	if !s.usePlatformBinding {
		link, err := s.repo.FindLink(ctx, session.TenantID, session.PlatformUserID)
		if s.authorizationDependencyUnavailable(ctx, err) {
			return ErrAuthorizationUnavailable
		}
		if err != nil || link == nil || link.Status != IdentityActive ||
			(session.CustomerID != 0 && link.CustomerID != session.CustomerID) {
			return ErrIdentityDisabled
		}
		session.CustomerID = link.CustomerID

	}
	if session.CustomerID == 0 {
		return ErrIdentityDisabled
	}
	if s.localAuthority != nil {
		if err := s.localAuthority.ValidateCustomer(ctx, session.TenantID, session.CustomerID, session.PlatformUserID, false); err != nil {
			if s.authorizationDependencyUnavailable(ctx, err) {
				return ErrAuthorizationUnavailable
			}
			return ErrIdentityDisabled
		}
	}
	return nil
}

// clearPortalAuthorization 清除有效授权投影但不触碰Cookie、凭据和原期限。
// 参数：session为已确定无有效门户授权的持锁会话。
// 返回值：无；旧检查点确保后续请求必须在线重验。
func clearPortalAuthorization(session *Session) {
	session.Roles, session.Permissions, session.DataScopes = []string{}, []string{}, []DataScope{}
	session.AuthorizationCheckedAt = time.Unix(1, 0).UTC()
}

// markPortalAuthorizationVerified 在会话事务提交后记录映射核验，避免会话与身份映射反向加锁。
// 参数：ctx为原请求，session为已保存的新授权快照。
// 返回值：记录成功为空；映射已失效拒绝访问，数据库故障为503。
func (s *Service) markPortalAuthorizationVerified(ctx context.Context, session *Session) error {
	if s.usePlatformBinding || isPortalSuperAdmin(session.Roles) {
		return nil
	}
	link, err := s.repo.FindLink(ctx, session.TenantID, session.PlatformUserID)
	if s.authorizationDependencyUnavailable(ctx, err) {
		return ErrAuthorizationUnavailable
	}
	if err != nil || link == nil || link.Status != IdentityActive || link.CustomerID != session.CustomerID {
		return ErrIdentityDisabled
	}
	if err := s.repo.MarkLinkVerified(ctx, session.TenantID, link.ID, session.AuthzRevision, s.clock.Now().UTC()); err != nil {
		if errors.Is(err, ErrIdentityDisabled) {
			return err
		}
		return ErrAuthorizationUnavailable
	}
	return nil
}

// authorizationDependencyUnavailable 为强制读取保留旧模式未知依赖故障的503语义。
// 参数：ctx为可信刷新上下文，err为存储或客户绑定检查的错误。
// 返回值：中央模式沿用现有策略，旧模式仅强制读将未知故障归为暂不可用。
func (s *Service) authorizationDependencyUnavailable(ctx context.Context, err error) bool {
	if s.dependencyUnavailable(err) {
		return true
	}
	return authorizationrefresh.Forced(ctx) && err != nil &&
		!errors.Is(err, ErrInvalidLoginState) && !errors.Is(err, ErrSessionNotFound) &&
		!errors.Is(err, ErrIdentityDisabled) && !errors.Is(err, ErrNotProvisioned) &&
		!errors.Is(err, ErrInvalidClaims) && !errors.Is(err, ErrPortalAuthorization)
}
