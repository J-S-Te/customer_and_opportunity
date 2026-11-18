package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

type authorizationRepository interface {
	WithAuthorizationLock(context.Context, string, time.Time, time.Duration,
		func(context.Context, *Session) error) (*Session, error)
}

// revalidateAuthorization 在凭据轮换提交后独立锁定并读取最新有效授权。
// 参数：ctx为请求，session为当前Cookie对应会话，锁内重读避免旧响应覆盖新权限。
// 返回值：原会话的新快照；拒绝清空授权且返回403，依赖故障保留会话并返回503。
func (s *Service) revalidateAuthorization(ctx context.Context, session *Session) (*Session, error) {
	repo, ok := s.repo.(authorizationRepository)
	if !ok {
		return nil, ErrAuthorizationUnavailable
	}
	updated, err := repo.WithAuthorizationLock(ctx, session.SessionIDHash, s.now().UTC(),
		s.localIdleTTL(), func(lockedCtx context.Context, current *Session) error {
			now := s.now().UTC()
			if !current.ExpiresAt.After(now) {
				return ErrUnauthenticated
			}
			if s.options.CentralClient != nil {
				if _, err := s.centralState(lockedCtx, current, false, false); err != nil {
					return err
				}
			} else if s.localIdleTTL() > 0 {
				interactive := current.CreatedAt
				if current.LastInteractiveAt != nil {
					interactive = *current.LastInteractiveAt
				}
				if !interactive.Add(s.localIdleTTL()).After(now) {
					return ErrUnauthenticated
				}
			}
			if !authorizationrefresh.Forced(ctx) && now.Sub(current.AuthorizationCheckedAt) < authorizationCheckInterval {
				return nil
			}
			return s.replaceAuthorization(lockedCtx, current, now)
		})
	if err != nil && !errors.Is(err, ErrUnauthenticated) &&
		!errors.Is(err, sharedauthorization.ErrForbidden) && !errors.Is(err, sharedauthorization.ErrTokenRejected) {
		return nil, ErrAuthorizationUnavailable
	}
	return updated, err
}

// replaceAuthorization 在持锁会话上核验稳定身份并替换有效权限。
// 参数：ctx为持锁请求，session为最新会话，now为锁内权威读取起点。
// 返回值：成功为空；无授权为403，身份错误为401，未知依赖错误为503。
func (s *Service) replaceAuthorization(ctx context.Context, session *Session, now time.Time) error {
	access, err := s.codec.decrypt(session.AccessTokenCipher)
	if err != nil {
		return ErrUnauthenticated
	}
	current, err := s.oidc.UserInfo(ctx, access)
	if errors.Is(err, sharedauthorization.ErrForbidden) {
		clearSessionAuthorization(session)
		return sharedauthorization.ErrForbidden
	}
	if errors.Is(err, sharedauthorization.ErrTokenRejected) {
		return err
	}
	if err != nil {
		return ErrAuthorizationUnavailable
	}
	if current.IdentityID != session.PlatformUserID || current.TenantID != session.TenantID ||
		current.PersonID != session.PersonID {
		return ErrUnauthenticated
	}
	if len(current.Roles) == 0 || len(current.Permissions) == 0 {
		clearSessionAuthorization(session)
		return sharedauthorization.ErrForbidden
	}
	current, err = normalizeAuthorization(current, s.options.TenantID, s.options.RoleConfigHash,
		s.options.EnvironmentCode, s.options.MaxRoles)
	if err != nil {
		return ErrUnauthenticated
	}
	session.RolesJSON, _ = json.Marshal(current.Roles)
	session.PermissionsJSON, _ = json.Marshal(current.Permissions)
	session.DataScopesJSON, _ = json.Marshal(current.DataScopes)
	session.OrganizationIDsJSON, _ = json.Marshal(current.OrganizationIDs)
	session.PrimaryOrgID, session.RoleConfigHash = current.PrimaryOrgID, current.RoleConfigHash
	session.AuthzRevision, session.AuthorizationCheckedAt = current.AuthzRevision, now
	if current.DisplayName != "" {
		session.DisplayName = current.DisplayName
	}
	if current.Username != "" {
		session.Username = current.Username
	}
	return nil
}

// clearSessionAuthorization 撤销可用授权投影但保留当前Cookie与原会话期限。
// 参数：session为已确认当前没有有效应用授权的持锁会话。
// 返回值：无；过期检查点确保后续请求重新读取授权，不能复用旧权限。
func clearSessionAuthorization(session *Session) {
	session.RolesJSON, session.PermissionsJSON = []byte("[]"), []byte("[]")
	session.DataScopesJSON, session.OrganizationIDsJSON = []byte("[]"), []byte("[]")
	session.PrimaryOrgID = ""
	session.AuthorizationCheckedAt = time.Unix(1, 0).UTC()
}
