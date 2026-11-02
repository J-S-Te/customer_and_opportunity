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
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"golang.org/x/oauth2"
)

const (
	loginTTL                   = 10 * time.Minute
	authorizationCheckInterval = 15 * time.Second
	authorizationMaxStale      = 60 * time.Second
	// sessionTokenRefreshLead 在访问令牌临近到期前提前刷新，避免平台授权上下文对过期令牌返回 401；
	// 实际刷新在会话行锁内串行化，多个并发请求不会同时消费同一个一次性 refresh token。
	sessionTokenRefreshLead = 60 * time.Second
)

var (
	ErrUnauthenticated          = errors.New("CRM session is not authenticated")
	ErrAuthorizationUnavailable = sharedauthorization.ErrUnavailable
)

// loginFailure 保持浏览器错误通用，同时保留
// 失败阶段和底层原因，供服务端结构化诊断。
// 组合错误同时维持 errors.Is(err, ErrUnauthenticated) 兼容性，
// 供仅关心认证分类的调用方使用。
func loginFailure(stage string, cause error) error {
	return fmt.Errorf("CRM OIDC login failed at %s: %w", stage, errors.Join(ErrUnauthenticated, cause))
}

type Options struct {
	CentralClient                             central.Client
	CentralExpectation                        central.Expectation
	TenantID, RoleConfigHash, EnvironmentCode string
	// SessionIdleTTL 与 SessionAbsoluteTTL 分别是浏览会话的空闲与绝对期限；
	// 绝对期限不再被访问令牌有效期截短，访问令牌只用于在线重验授权并会提前刷新。
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
	MaxRoles           int
}

type Service struct {
	repo    repository
	oidc    oidcClient
	broker  brokerVerifier
	codec   *secretCodec
	options Options
	now     func() time.Time
}

// NewService 装配会话服务，并在旧模式验证本地策略、中央模式验证事务与协议能力。
// 参数：repo为存储，oidc为协议客户端，encryptionKey为加密密钥，options为策略，brokers为代理校验器。
// 返回值：已装配服务；策略或依赖能力不满足时返回错误。
func NewService(repo repository, oidc oidcClient, encryptionKey []byte, options Options, brokers ...brokerVerifier) (*Service, error) {
	if options.CentralClient == nil && (options.SessionIdleTTL <= 0 || options.SessionAbsoluteTTL <= options.SessionIdleTTL) {
		return nil, errors.New("CRM session idle and absolute durations are invalid")
	}
	codec, err := newSecretCodec(encryptionKey)
	if err != nil {
		return nil, err
	}
	var broker brokerVerifier
	if len(brokers) > 0 {
		broker = brokers[0]
	}
	if options.CentralClient != nil {
		if _, ok := repo.(governanceRepository); !ok {
			return nil, errors.New("central sessions require transactional storage")
		}
		if _, ok := oidc.(centralExchanger); !ok {
			return nil, errors.New("central sessions require bound OIDC exchange")
		}
		if _, ok := oidc.(tokenRefresher); !ok {
			return nil, errors.New("central sessions require token refresh")
		}
	}
	return &Service{repo: repo, oidc: oidc, broker: broker, codec: codec, options: options, now: time.Now}, nil
}

type LoginStart struct {
	AuthorizationURL string
	State            string
}

// BeginLogin 建立一次性登录事务并生成当前模式的可信认证入口。
// 参数：ctx为请求，returnPath为同源返回路径，forceLogin为是否强制重新认证。
// 返回值：原始state绑定的认证入口；生成随机材料或存储失败时返回错误。
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
	forcedLogin := len(forceLogin) > 0 && forceLogin[0]
	if err := s.repo.SaveLogin(ctx, &LoginTransaction{StateHash: tokenHash(state), TenantID: s.options.TenantID, NonceCipher: nonceCipher, CodeVerifierCipher: verifierCipher, ReturnPath: returnPath, ExpiresAt: now.Add(loginTTL), CreatedAt: now, ForceLogin: forcedLogin}); err != nil {
		return LoginStart{}, err
	}
	if s.options.CentralClient != nil {
		return LoginStart{AuthorizationURL: s.options.CentralClient.BeginURL(state, nonce), State: state}, nil
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

// CompleteLogin 消费原登录事务，核验授权并按当前模式建立本地会话。
// 参数：ctx为请求，state为原状态，code为授权码，callbackLoginIP为规范化回调来源。
// 返回值：登录结果；身份、绑定、持久化或依赖失败返回错误。
func (s *Service) CompleteLogin(ctx context.Context, state, code string, callbackLoginIP ...string) (LoginResult, error) {
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
	var claims verifiedClaims
	if s.options.CentralClient != nil {
		ticket, ticketErr := s.codec.decrypt(transaction.CentralTicketCipher)
		if ticketErr != nil || ticket == "" {
			return LoginResult{}, loginFailure("central_ticket", ErrUnauthenticated)
		}
		claims, err = s.oidc.(centralExchanger).ExchangeWithCentral(ctx, code, verifier, nonce, ticket)
	} else {
		claims, err = s.oidc.Exchange(ctx, code, verifier, nonce)
	}
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
	// 浏览器通过受管反向代理访问此回调。经校验的
	// 公开地址是新 CRM 会话的权威登录地址；
	// 平台授权上下文中的值仅作为
	// 非 HTTP 调用方及向后兼容的回退。
	if len(callbackLoginIP) > 0 {
		if value := loginip.Normalize(callbackLoginIP[0]); value != "" {
			claims.LoginIP = value
		}
	}
	claims.LoginIP = loginip.Normalize(claims.LoginIP)
	// 此步骤必须位于全部 CRM 授权码、令牌和声明检查之后。
	// 平台回执失败必须在部署门禁中可见，但不能
	// 将无效令牌变为有效 CRM 会话，也不能绕过上述检查。
	if s.broker != nil {
		if err := s.broker.Verify(ctx, claims); err != nil {
			slog.Default().Warn("CRM Keycloak broker verification callback failed", "identity_id", claims.IdentityID, "application_code", "customer_and_opportunity", "error", err)
		}
	}
	// 本地会话按统一策略使用绝对期限，不再被短命访问令牌截短；服务端会用 refresh token 在
	// 访问令牌到期前刷新。若提供方没有签发 refresh token（客户端未开启 refresh 授权），
	// 只能回退到旧语义，以本次令牌到期时间封顶，避免产生无法在线重验却看似有效的长会话。
	expiresAt := now.Add(s.options.SessionAbsoluteTTL)
	if strings.TrimSpace(claims.RefreshToken) == "" {
		expiresAt = earliestExpiry(claims.ExpiresAt, expiresAt)
		slog.Default().Warn("CRM OIDC login has no refresh token; session is capped by access token expiry", "application_code", "customer_and_opportunity")
	}
	if s.options.CentralClient != nil {
		if central.Validate(claims.CentralBinding.Context, s.options.CentralExpectation, claims.IdentityID) != nil || claims.CentralBinding.Token == "" || claims.RefreshToken == "" {
			return LoginResult{}, loginFailure("central_binding", ErrUnauthenticated)
		}
		expiresAt = claims.CentralBinding.Context.AbsoluteExpiresAt
	}
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
	var refreshTokenCipher []byte
	if strings.TrimSpace(claims.RefreshToken) != "" {
		refreshTokenCipher, err = s.codec.encrypt(claims.RefreshToken)
		if err != nil {
			return LoginResult{}, err
		}
	}
	accessTokenExpiresAt := claims.ExpiresAt.UTC()
	lastInteractiveAt := now
	session := &Session{SessionIDHash: tokenHash(rawSession), TenantID: claims.TenantID, PlatformUserID: claims.IdentityID, OIDCSessionID: claims.OIDCSessionID, PersonID: claims.PersonID, DisplayName: claims.DisplayName, Username: claims.Username, LoginIP: claims.LoginIP, PrimaryOrgID: claims.PrimaryOrgID, OrganizationIDsJSON: organizationIDsJSON, RolesJSON: rolesJSON, PermissionsJSON: permissionsJSON, DataScopesJSON: dataScopesJSON, RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision, AccessTokenCipher: accessTokenCipher, RefreshTokenCipher: refreshTokenCipher, AccessTokenExpiresAt: &accessTokenExpiresAt, ExpiresAt: expiresAt, AuthorizationCheckedAt: now, CreatedAt: now, LastSeenAt: now, LastInteractiveAt: &lastInteractiveAt}
	if s.options.CentralClient != nil {
		session.CentralBindingCipher, err = s.codec.encrypt(claims.CentralBinding.Token)
		if err != nil {
			return LoginResult{}, err
		}
		session.RefreshTokenCipher, err = s.codec.encrypt(claims.RefreshToken)
		if err != nil {
			return LoginResult{}, err
		}
		centralTokenExpiry := claims.AccessTokenExpiresAt.UTC()
		session.AccessTokenExpiresAt = &centralTokenExpiry
		session.CentralSessionID = claims.CentralBinding.Context.SessionID
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{SessionToken: rawSession, ReturnPath: transaction.ReturnPath, ExpiresAt: expiresAt}, nil
}

// Authenticate 校验本地会话、当前模式权威期限和业务授权。
// 参数：ctx为请求与取消边界，rawSession为浏览器Cookie中的随机会话材料。
// 返回值：已核验主体；明确失效返回未认证，临时依赖错误返回不可用。
func (s *Service) Authenticate(ctx context.Context, rawSession string) (sharedauth.Principal, error) {
	if strings.TrimSpace(rawSession) == "" {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	now := s.now().UTC()
	hash := tokenHash(rawSession)
	session, err := s.repo.FindSession(ctx, hash, now, s.localIdleTTL())
	if err != nil {
		return unavailablePrincipal(err)
	}
	if session.TenantID != s.options.TenantID {
		return sharedauth.Principal{}, ErrUnauthenticated
	}
	if s.options.CentralClient != nil {
		if _, err = s.centralState(ctx, session, false); err != nil {
			return sharedauth.Principal{}, err
		}
		session, err = s.refreshSessionAccess(ctx, session)
		if err != nil {
			return sharedauth.Principal{}, err
		}
	} else if sessionNeedsTokenRefresh(session, now) {
		refreshed, refreshErr := s.refreshSessionTokens(ctx, hash, now, "")
		if refreshErr != nil {
			return sharedauth.Principal{}, s.refreshFailure(ctx, hash, now, refreshErr)
		}
		session = refreshed
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
		if s.options.CentralClient == nil && errors.Is(currentErr, sharedauthorization.ErrTokenRejected) {
			// 访问令牌可能在两次重验之间过期：在会话行锁内确认未被其他请求刷新后再尝试一次。
			if refreshed, refreshErr := s.refreshSessionTokens(ctx, session.SessionIDHash, now, accessToken); refreshErr == nil {
				session = refreshed
				if accessToken, decryptErr = s.codec.decrypt(session.AccessTokenCipher); decryptErr != nil {
					_ = s.repo.RevokeSession(ctx, session.SessionIDHash, now)
					return sharedauth.Principal{}, ErrUnauthenticated
				}
				current, currentErr = s.oidc.UserInfo(ctx, accessToken)
			} else {
				// 与预刷新一致：提供方明确拒绝则撤销本地会话，平台暂时不可用则保留会话。
				currentErr = s.refreshFailure(ctx, session.SessionIDHash, now, refreshErr)
			}
		}
		if errors.Is(currentErr, sharedauthorization.ErrUnavailable) {
			if s.options.CentralClient != nil || now.Sub(session.AuthorizationCheckedAt) > authorizationMaxStale {
				return sharedauth.Principal{}, ErrAuthorizationUnavailable
			}
			if err := s.repo.TouchSession(ctx, session.SessionIDHash, now, time.Time{}, s.localIdleTTL()); err != nil {
				return unavailablePrincipal(err)
			}
			return s.sessionPrincipal(stored, session), nil
		}
		if currentErr != nil {
			if !errors.Is(currentErr, ErrUnauthenticated) {
				_ = s.repo.RevokeSession(ctx, session.SessionIDHash, now)
			}
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		current, currentErr = normalizeAuthorization(current, s.options.TenantID, s.options.RoleConfigHash, s.options.EnvironmentCode, s.options.MaxRoles)
		if currentErr != nil || !sameAuthorization(stored, current) {
			_ = s.repo.RevokeSessionsForSubject(ctx, session.TenantID, session.PlatformUserID, now)
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		checkedAt = now
	}
	if err := s.repo.TouchSession(ctx, session.SessionIDHash, now, checkedAt, s.localIdleTTL()); err != nil {
		return unavailablePrincipal(err)
	}
	return s.sessionPrincipal(stored, session), nil
}

// localIdleTTL 仅在旧模式提供本地空闲窗口，中央模式由平台权威判定空闲。
// 参数：无。
// 返回值：旧模式的空闲时长；中央模式返回零以停用本地空闲推算。
func (s *Service) localIdleTTL() time.Duration {
	if s.options.CentralClient != nil {
		return 0
	}
	return s.options.SessionIdleTTL
}

// SessionActivity 是显式活动接口返回的权威空闲窗口。Remaining 已按绝对期限收窄且不为负。
type SessionActivity struct {
	IdleTimeout time.Duration
	Remaining   time.Duration
}

// RecordActivity 按模式代理中央真实活动，或推进旧模式的本地交互窗口。
// 参数：ctx为请求，rawSession为通过同源写保护的浏览器Cookie。
// 返回值：权威空闲窗口；已过期或撤销返回未认证，临时故障返回不可用。
func (s *Service) RecordActivity(ctx context.Context, rawSession string) (SessionActivity, error) {
	if strings.TrimSpace(rawSession) == "" {
		return SessionActivity{}, ErrUnauthenticated
	}
	if s.options.CentralClient != nil {
		state, err := s.Lifecycle(ctx, rawSession, true)
		if err != nil {
			return SessionActivity{}, err
		}
		return SessionActivity{
			IdleTimeout: time.Duration(state.IdleTimeoutSeconds) * time.Second,
			Remaining:   time.Duration(state.IdleRemainingMS) * time.Millisecond,
		}, nil
	}
	now := s.now().UTC()
	deadline, err := s.repo.RecordActivity(ctx, tokenHash(rawSession), now, s.options.SessionIdleTTL)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return SessionActivity{}, ErrUnauthenticated
		}
		return SessionActivity{}, fmt.Errorf("record CRM session activity: %w", ErrAuthorizationUnavailable)
	}
	remaining := deadline.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	return SessionActivity{IdleTimeout: s.options.SessionIdleTTL, Remaining: remaining}, nil
}

// sessionPrincipal 为旧模式主体附加本地空闲窗口，中央模式通过生命周期接口投影。
// 参数：claims为已核验授权，session为本地持久会话。
// 返回值：认证主体；空闲字段不参与权限判断。
func (s *Service) sessionPrincipal(claims verifiedClaims, session *Session) sharedauth.Principal {
	principal := principalFromClaims(claims)
	if s.options.CentralClient != nil {
		return principal
	}
	principal.SessionIdleTimeout = s.options.SessionIdleTTL
	lastInteractive := session.CreatedAt
	if session.LastInteractiveAt != nil {
		lastInteractive = *session.LastInteractiveAt
	}
	principal.SessionIdleDeadline = sessionIdleDeadlineAt(lastInteractive, s.options.SessionIdleTTL, session.ExpiresAt)
	return principal
}

// sessionIdleDeadlineAt 取"最后交互 + 空闲策略"与绝对到期中的较早者，和 /auth/me 的剩余时间一致。
func sessionIdleDeadlineAt(lastInteractive time.Time, idleTTL time.Duration, absolute time.Time) time.Time {
	deadline := lastInteractive.UTC().Add(idleTTL)
	if absolute.UTC().Before(deadline) {
		return absolute.UTC()
	}
	return deadline
}

// sessionNeedsTokenRefresh 仅对已保存 refresh token 与令牌到期时间的会话提前刷新；
// 升级前的存量行两列为 NULL，保持原有"重验失败即退出"的行为自然到期。
func sessionNeedsTokenRefresh(session *Session, now time.Time) bool {
	if session == nil || len(session.RefreshTokenCipher) == 0 || session.AccessTokenExpiresAt == nil {
		return false
	}
	return !session.AccessTokenExpiresAt.After(now.Add(sessionTokenRefreshLead))
}

// refreshSessionTokens 在会话行锁内刷新访问令牌。staleAccessToken 非空表示在线重验刚被拒绝：
// 只有持久化令牌仍是那枚被拒令牌时才强制刷新，避免并发请求重复消费一次性 refresh token。
func (s *Service) refreshSessionTokens(ctx context.Context, hash string, now time.Time, staleAccessToken string) (*Session, error) {
	return s.repo.RefreshSessionTokens(ctx, hash, now, s.options.SessionIdleTTL, func(current *Session) (SessionTokens, bool, error) {
		stale := staleAccessToken != ""
		if stale {
			currentAccess, err := s.codec.decrypt(current.AccessTokenCipher)
			if err != nil {
				return SessionTokens{}, false, err
			}
			stale = currentAccess == staleAccessToken
		}
		if !stale && !sessionNeedsTokenRefresh(current, now) {
			return SessionTokens{}, false, nil
		}
		return s.refreshTokens(ctx, current)
	})
}

func (s *Service) refreshTokens(ctx context.Context, current *Session) (SessionTokens, bool, error) {
	if len(current.RefreshTokenCipher) == 0 {
		return SessionTokens{}, false, errOIDCRefreshRejected
	}
	refreshToken, err := s.codec.decrypt(current.RefreshTokenCipher)
	if err != nil || strings.TrimSpace(refreshToken) == "" {
		return SessionTokens{}, false, errOIDCRefreshRejected
	}
	refreshed, err := s.oidc.RefreshToken(ctx, refreshToken)
	if err != nil {
		return SessionTokens{}, false, err
	}
	accessCipher, err := s.codec.encrypt(refreshed.AccessToken)
	if err != nil {
		return SessionTokens{}, false, err
	}
	refreshCipher, err := s.codec.encrypt(refreshed.RefreshToken)
	if err != nil {
		return SessionTokens{}, false, err
	}
	expiresAt := refreshed.ExpiresAt.UTC()
	if expiresAt.IsZero() || expiresAt.After(current.ExpiresAt) {
		// 访问令牌期限不能越过本地绝对期限；提供方未返回到期时间时按会话绝对期限保证再次重验。
		expiresAt = current.ExpiresAt.UTC()
	}
	return SessionTokens{AccessTokenCipher: accessCipher, RefreshTokenCipher: refreshCipher, AccessTokenExpiresAt: expiresAt}, true, nil
}

// refreshFailure 把刷新失败映射为不会放大权限的错误：提供方明确拒绝 refresh token 时撤销本地会话；
// 会话已在行锁内消失按未认证；其余（网络、平台 5xx）保留会话并返回依赖不可用，等待下一次快速重试。
func (s *Service) refreshFailure(ctx context.Context, hash string, now time.Time, refreshErr error) error {
	if errors.Is(refreshErr, errNotFound) {
		return ErrUnauthenticated
	}
	if errors.Is(refreshErr, errOIDCRefreshRejected) {
		_ = s.repo.RevokeSession(ctx, hash, now)
		return ErrUnauthenticated
	}
	slog.Default().Warn("CRM session access token refresh is temporarily unavailable", "application_code", "customer_and_opportunity")
	return ErrAuthorizationUnavailable
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
	return verifiedClaims{Subject: session.PlatformUserID, IdentityID: session.PlatformUserID, TenantID: session.TenantID, PersonID: session.PersonID, DisplayName: session.DisplayName, Username: session.Username, LoginIP: loginip.Normalize(session.LoginIP), PrimaryOrgID: session.PrimaryOrgID, OrganizationIDs: organizationIDs, Roles: roles, Permissions: permissions, DataScopes: dataScopes, RoleConfigHash: session.RoleConfigHash, AuthzRevision: session.AuthzRevision}, nil
}

func principalFromClaims(claims verifiedClaims) sharedauth.Principal {
	permissions := make(map[string]struct{}, len(claims.Permissions))
	for _, permission := range claims.Permissions {
		permissions[permission] = struct{}{}
	}
	scope := scopeForDataScopes(claims.DataScopes)
	// PersonID 只能来自平台显式、租户内的人员绑定；没有绑定时保持为空，绝不能由 sub 猜测，
	// 否则售前任务等以人员身份授权的数据会被错误归属。
	return sharedauth.Principal{UserID: claims.Subject, PersonID: claims.PersonID, TenantID: claims.TenantID, DisplayName: claims.DisplayName, Username: claims.Username, LoginIP: claims.LoginIP, PrimaryOrgID: claims.PrimaryOrgID, Roles: append([]string(nil), claims.Roles...), Permissions: permissions, DataScopes: append([]sharedauth.DataScope(nil), claims.DataScopes...), ScopeMode: scope, OrganizationIDs: append([]string(nil), claims.OrganizationIDs...), RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision}
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
