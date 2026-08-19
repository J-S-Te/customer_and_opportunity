package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"golang.org/x/oauth2"
)

const (
	loginTTL                   = 10 * time.Minute
	authorizationCheckInterval = 15 * time.Second
	authorizationMaxStale      = 60 * time.Second
)

var (
	ErrUnauthenticated          = errors.New("CRM session is not authenticated")
	ErrAuthorizationUnavailable = sharedauthorization.ErrUnavailable
)

// loginFailure keeps the browser-facing error generic while preserving the
// failed stage and the underlying cause for structured server diagnostics.
// The joined errors also keep errors.Is(err, ErrUnauthenticated) compatible
// with callers that only need the authentication classification.
func loginFailure(stage string, cause error) error {
	return fmt.Errorf("CRM OIDC login failed at %s: %w", stage, errors.Join(ErrUnauthenticated, cause))
}

type Options struct {
	TenantID, RoleConfigHash, EnvironmentCode string
	SessionTTL                                time.Duration
	MaxRoles                                  int
}

type Service struct {
	repo    repository
	oidc    oidcClient
	broker  brokerVerifier
	codec   *secretCodec
	options Options
	now     func() time.Time
}

func NewService(repo repository, oidc oidcClient, encryptionKey []byte, options Options, brokers ...brokerVerifier) (*Service, error) {
	codec, err := newSecretCodec(encryptionKey)
	if err != nil {
		return nil, err
	}
	var broker brokerVerifier
	if len(brokers) > 0 {
		broker = brokers[0]
	}
	return &Service{repo: repo, oidc: oidc, broker: broker, codec: codec, options: options, now: time.Now}, nil
}

type LoginStart struct{ AuthorizationURL string }

func (s *Service) BeginLogin(ctx context.Context, returnPath string, forceLogin ...bool) (LoginStart, error) {
	// return_to 最终会进入重定向响应，只允许站内绝对路径，阻断开放重定向和响应头注入。
	if !safeReturnPath(returnPath) {
		returnPath = "/"
	}
	state, err := randomToken(32)
	if err != nil {
		return LoginStart{}, err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return LoginStart{}, err
	}
	verifier := oauth2.GenerateVerifier()
	nonceCipher, err := s.codec.encrypt(nonce)
	if err != nil {
		return LoginStart{}, err
	}
	verifierCipher, err := s.codec.encrypt(verifier)
	if err != nil {
		return LoginStart{}, err
	}
	now := s.now().UTC()
	// state 原文仅返回浏览器；服务端保存摘要并加密其余秘密，回调消费后立即删除。
	if err := s.repo.SaveLogin(ctx, &LoginTransaction{StateHash: tokenHash(state), TenantID: s.options.TenantID, NonceCipher: nonceCipher, CodeVerifierCipher: verifierCipher, ReturnPath: returnPath, ExpiresAt: now.Add(loginTTL), CreatedAt: now}); err != nil {
		return LoginStart{}, err
	}
	if forced, ok := s.oidc.(forcedLoginOIDCClient); ok && len(forceLogin) > 0 && forceLogin[0] {
		return LoginStart{AuthorizationURL: forced.AuthorizationURLWithPrompt(state, nonce, verifier, true)}, nil
	}
	return LoginStart{AuthorizationURL: s.oidc.AuthorizationURL(state, nonce, verifier)}, nil
}

type LoginResult struct {
	SessionToken, ReturnPath string
	ExpiresAt                time.Time
}

func (s *Service) CompleteLogin(ctx context.Context, state, code string) (LoginResult, error) {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(code) == "" {
		return LoginResult{}, loginFailure("callback_parameters", errors.New("state or code is missing"))
	}
	now := s.now().UTC()
	// 先原子消费 state，再兑换授权码；即使后续校验失败，同一回调也不能重放。
	transaction, err := s.repo.ConsumeLogin(ctx, tokenHash(state), now)
	if err != nil {
		return LoginResult{}, loginFailure("login_state", err)
	}
	nonce, err := s.codec.decrypt(transaction.NonceCipher)
	if err != nil {
		return LoginResult{}, loginFailure("nonce_unwrap", err)
	}
	verifier, err := s.codec.decrypt(transaction.CodeVerifierCipher)
	if err != nil {
		return LoginResult{}, loginFailure("pkce_verifier_unwrap", err)
	}
	claims, err := s.oidc.Exchange(ctx, code, verifier, nonce)
	if err != nil {
		return LoginResult{}, loginFailure("authorization_code_exchange", err)
	}
	// 不直接信任平台返回的扁平权限：必须与本地发布的角色目录精确一致，防止目录漂移或越权声明。
	claims, err = normalizeAuthorization(claims, s.options.TenantID, s.options.RoleConfigHash, s.options.EnvironmentCode, s.options.MaxRoles)
	if err != nil || !claims.ExpiresAt.After(now) {
		if err == nil {
			err = errors.New("authorization claims are expired")
		}
		return LoginResult{}, loginFailure("authorization_claims", err)
	}
	// This is deliberately after all CRM code/token/claims checks.  A failed
	// platform receipt must remain visible to its deployment gate, but cannot
	// turn an invalid token into a valid CRM session or bypass the checks above.
	if s.broker != nil {
		if err := s.broker.Verify(ctx, claims); err != nil {
			slog.Default().Warn("CRM Keycloak broker verification callback failed", "identity_id", claims.IdentityID, "application_code", "customer_and_opportunity", "error", err)
		}
	}
	// 本地会话绝不能比上游 ID/Access Token 或本地策略 TTL 活得更久。
	expiresAt := earliestExpiry(claims.ExpiresAt, now.Add(s.options.SessionTTL))
	if !expiresAt.After(now) {
		return LoginResult{}, loginFailure("session_expiry", errors.New("session expiry is not in the future"))
	}
	rawSession, err := randomToken(48)
	if err != nil {
		return LoginResult{}, err
	}
	rolesJSON, err := json.Marshal(claims.Roles)
	if err != nil {
		return LoginResult{}, err
	}
	permissionsJSON, err := json.Marshal(claims.Permissions)
	if err != nil {
		return LoginResult{}, err
	}
	dataScopesJSON, err := json.Marshal(claims.DataScopes)
	if err != nil {
		return LoginResult{}, err
	}
	organizationIDsJSON, err := json.Marshal(claims.OrganizationIDs)
	if err != nil {
		return LoginResult{}, err
	}
	accessTokenCipher, err := s.codec.encrypt(claims.AccessToken)
	if err != nil {
		return LoginResult{}, err
	}
	session := &Session{SessionIDHash: tokenHash(rawSession), TenantID: claims.TenantID, PlatformUserID: claims.IdentityID, PersonID: claims.PersonID, DisplayName: claims.DisplayName, PrimaryOrgID: claims.PrimaryOrgID, OrganizationIDsJSON: organizationIDsJSON, RolesJSON: rolesJSON, PermissionsJSON: permissionsJSON, DataScopesJSON: dataScopesJSON, RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision, AccessTokenCipher: accessTokenCipher, ExpiresAt: expiresAt, AuthorizationCheckedAt: now, CreatedAt: now, LastSeenAt: now}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{SessionToken: rawSession, ReturnPath: transaction.ReturnPath, ExpiresAt: expiresAt}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawSession string) (sharedauth.Principal, error) {
	if strings.TrimSpace(rawSession) == "" {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	now := s.now().UTC()
	session, err := s.repo.FindSession(ctx, tokenHash(rawSession), now)
	if err != nil || session.TenantID != s.options.TenantID {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	stored, err := claimsFromSession(session)
	if err != nil {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	checkedAt := time.Time{}
	// 会话不是永久授权缓存。超过检查窗口后通过 UserInfo 重新取得完整授权快照；任一字段变化都撤销
	// 该主体的全部 CRM 会话，使禁用账号、组织调整和权限降级在短窗口内统一生效。
	if now.Sub(session.AuthorizationCheckedAt) >= authorizationCheckInterval {
		accessToken, decryptErr := s.codec.decrypt(session.AccessTokenCipher)
		if decryptErr != nil {
			_ = s.repo.RevokeSession(ctx, session.SessionIDHash, now)
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		current, currentErr := s.oidc.UserInfo(ctx, accessToken)
		if errors.Is(currentErr, sharedauthorization.ErrUnavailable) {
			if now.Sub(session.AuthorizationCheckedAt) > authorizationMaxStale {
				return sharedauth.Principal{}, ErrAuthorizationUnavailable
			}
			if err := s.repo.TouchSession(ctx, session.SessionIDHash, now, time.Time{}); err != nil {
				return sharedauth.Principal{}, ErrUnauthenticated
			}
			return principalFromClaims(stored), nil
		}
		if currentErr != nil {
			_ = s.repo.RevokeSession(ctx, session.SessionIDHash, now)
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		current, currentErr = normalizeAuthorization(current, s.options.TenantID, s.options.RoleConfigHash, s.options.EnvironmentCode, s.options.MaxRoles)
		if currentErr != nil || !sameAuthorization(stored, current) {
			_ = s.repo.RevokeSessionsForSubject(ctx, session.TenantID, session.PlatformUserID, now)
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		checkedAt = now
	}
	if err := s.repo.TouchSession(ctx, session.SessionIDHash, now, checkedAt); err != nil {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	return principalFromClaims(stored), nil
}

func (s *Service) Logout(ctx context.Context, rawSession string) {
	if rawSession != "" {
		_ = s.repo.RevokeSession(ctx, tokenHash(rawSession), s.now().UTC())
	}
}

func claimsFromSession(session *Session) (verifiedClaims, error) {
	var organizationIDs, roles, permissions []string
	var dataScopes []sharedauth.DataScope
	if err := json.Unmarshal(session.OrganizationIDsJSON, &organizationIDs); err != nil {
		return verifiedClaims{}, err
	}
	if err := json.Unmarshal(session.RolesJSON, &roles); err != nil {
		return verifiedClaims{}, err
	}
	if err := json.Unmarshal(session.PermissionsJSON, &permissions); err != nil {
		return verifiedClaims{}, err
	}
	if err := json.Unmarshal(session.DataScopesJSON, &dataScopes); err != nil {
		return verifiedClaims{}, err
	}
	return verifiedClaims{Subject: session.PlatformUserID, IdentityID: session.PlatformUserID, TenantID: session.TenantID, PersonID: session.PersonID, DisplayName: session.DisplayName, PrimaryOrgID: session.PrimaryOrgID, OrganizationIDs: organizationIDs, Roles: roles, Permissions: permissions, DataScopes: dataScopes, RoleConfigHash: session.RoleConfigHash, AuthzRevision: session.AuthzRevision}, nil
}

func principalFromClaims(claims verifiedClaims) sharedauth.Principal {
	permissions := make(map[string]struct{}, len(claims.Permissions))
	for _, permission := range claims.Permissions {
		permissions[permission] = struct{}{}
	}
	scope := scopeForDataScopes(claims.DataScopes)
	// PersonID 只能来自平台显式、租户内的人员绑定；没有绑定时保持为空，绝不能由 sub 猜测，
	// 否则售前任务等以人员身份授权的数据会被错误归属。
	return sharedauth.Principal{UserID: claims.Subject, PersonID: claims.PersonID, TenantID: claims.TenantID, DisplayName: claims.DisplayName, PrimaryOrgID: claims.PrimaryOrgID, Roles: append([]string(nil), claims.Roles...), Permissions: permissions, DataScopes: append([]sharedauth.DataScope(nil), claims.DataScopes...), ScopeMode: scope, OrganizationIDs: append([]string(nil), claims.OrganizationIDs...), RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision}
}

func scopeForDataScopes(scopes []sharedauth.DataScope) sharedauth.ScopeMode {
	for _, scope := range scopes {
		switch scope.ScopeType {
		case sharedauthorization.ScopeApplication, sharedauthorization.ScopeEnvironment, sharedauthorization.ScopeTenant:
			return sharedauth.ScopeAll
		}
	}
	for _, scope := range scopes {
		if scope.ScopeType == sharedauthorization.ScopeOrg {
			return sharedauth.ScopeOrg
		}
	}
	return sharedauth.ScopeSelf
}

func safeReturnPath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.ContainsAny(path, "\r\n")
}
