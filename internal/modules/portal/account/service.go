package account

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

const portalRole = "portal_customer"

const authorizationCheckInterval = 15 * time.Second
const authorizationMaxStale = 60 * time.Second

type Service struct {
	repo            Repository
	oidc            OIDCClient
	invites         InviteClient
	protector       SecretProtector
	clock           Clock
	random          RandomSource
	roleConfigHash  string
	maxSessionAge   time.Duration
	environmentCode string
	// usePlatformBinding 打开后，非邀请登录以平台 authorization-context 下发的
	// customer_ref 作为客户边界，不再依赖本地 portal_identity_links（Phase 4）。
	usePlatformBinding bool
}

// ServiceOption 允许装配层在 Phase 4 过渡期打开平台绑定路径；默认关闭保持旧行为。
type ServiceOption func(*Service)

// WithPlatformBinding 打开平台客户绑定路径（nil 语义：调用即开启）。
func WithPlatformBinding() ServiceOption {
	return func(service *Service) { service.usePlatformBinding = true }
}

func NewService(repo Repository, oidc OIDCClient, invites InviteClient, protector SecretProtector, clock Clock, random RandomSource, roleConfigHash string, maxSessionAge time.Duration, environmentCodes ...string) *Service {
	environmentCode := "dev"
	if len(environmentCodes) > 0 && strings.TrimSpace(environmentCodes[0]) != "" {
		environmentCode = strings.TrimSpace(environmentCodes[0])
	}
	service := &Service{repo: repo, oidc: oidc, invites: invites, protector: protector, clock: clock, random: random, roleConfigHash: roleConfigHash, maxSessionAge: maxSessionAge, environmentCode: environmentCode}
	return service
}

// NewServiceWithOptions 是带选项的构造入口；保留 NewService 供既有装配与测试使用。
func NewServiceWithOptions(repo Repository, oidc OIDCClient, invites InviteClient, protector SecretProtector, clock Clock, random RandomSource, roleConfigHash string, maxSessionAge time.Duration, environmentCode string, options ...ServiceOption) *Service {
	service := NewService(repo, oidc, invites, protector, clock, random, roleConfigHash, maxSessionAge, environmentCode)
	for _, apply := range options {
		apply(service)
	}
	return service
}

type ProvisionCommand struct {
	TenantID, AccountNo, PlatformUserID, DisplayName string
	CustomerID                                       uint64
	ContactID                                        *uint64
}

type ProvisionResult struct {
	PortalAccountID string `json:"portal_account_id"`
	AccountNo       string `json:"account_no"`
}

type DisableCommand struct {
	TenantID, PlatformUserID, ActorID, Reason, IdempotencyKey string
	CustomerID                                                uint64
}

type DisableResult struct {
	CustomerID     uint64         `json:"customer_id"`
	PlatformUserID string         `json:"platform_user_id"`
	Status         IdentityStatus `json:"status"`
	Version        uint64         `json:"version"`
}

type ReconciliationSnapshot struct {
	PlatformUserID  string         `json:"platform_user_id"`
	Found           bool           `json:"found"`
	PortalAccountID string         `json:"portal_account_id,omitempty"`
	AccountNo       string         `json:"account_no,omitempty"`
	CustomerID      uint64         `json:"customer_id,omitempty"`
	ContactID       *uint64        `json:"contact_id,omitempty"`
	Status          IdentityStatus `json:"status,omitempty"`
	Version         uint64         `json:"version,omitempty"`
}

// ReconciliationSnapshots is a bounded, machine-only read projection. Missing
// subjects are returned explicitly and no search, contact details, tokens or
// display names are exposed.
func (s *Service) ReconciliationSnapshots(ctx context.Context, tenantID string, subjects []string) ([]ReconciliationSnapshot, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || tenantID != strings.TrimSpace(tenantID) || len(subjects) == 0 || len(subjects) > 100 {
		return nil, ErrInvalidClaims
	}
	canonical := make([]string, 0, len(subjects))
	seen := make(map[string]struct{}, len(subjects))
	for _, raw := range subjects {
		subject := strings.TrimSpace(raw)
		if subject == "" || subject != raw || len(subject) > 128 {
			return nil, ErrInvalidClaims
		}
		if _, duplicate := seen[subject]; duplicate {
			return nil, ErrInvalidClaims
		}
		seen[subject] = struct{}{}
		canonical = append(canonical, subject)
	}
	repo, ok := s.repo.(IdentityReconciliationRepository)
	if !ok || repo == nil {
		return nil, ErrNotProvisioned
	}
	links, err := repo.FindLinksBySubjects(ctx, tenantID, canonical)
	if err != nil {
		return nil, err
	}
	bySubject := make(map[string]IdentityLink, len(links))
	for _, link := range links {
		if link.TenantID != tenantID {
			return nil, ErrInvalidClaims
		}
		if _, requested := seen[link.PlatformUserID]; !requested {
			return nil, ErrInvalidClaims
		}
		if _, duplicate := bySubject[link.PlatformUserID]; duplicate {
			return nil, ErrInvalidClaims
		}
		bySubject[link.PlatformUserID] = link
	}
	result := make([]ReconciliationSnapshot, 0, len(canonical))
	for _, subject := range canonical {
		link, found := bySubject[subject]
		if !found {
			result = append(result, ReconciliationSnapshot{PlatformUserID: subject, Found: false})
			continue
		}
		result = append(result, ReconciliationSnapshot{
			PlatformUserID: subject, Found: true, PortalAccountID: portalAccountID(link.ID),
			AccountNo: link.AccountNo, CustomerID: link.CustomerID, ContactID: link.ContactID,
			Status: link.Status, Version: link.Version,
		})
	}
	return result, nil
}

// Disable 先冻结 Portal 本地身份映射，再由调用方撤销平台角色。
// 即使远端步骤需要由持久化 Saga 重试，本地会话边界也已关闭，不会继续放行旧权限。
func (s *Service) Disable(ctx context.Context, cmd DisableCommand) (DisableResult, error) {
	cmd.TenantID = strings.TrimSpace(cmd.TenantID)
	cmd.PlatformUserID = strings.TrimSpace(cmd.PlatformUserID)
	cmd.ActorID = strings.TrimSpace(cmd.ActorID)
	cmd.Reason = strings.TrimSpace(cmd.Reason)
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if cmd.TenantID == "" || cmd.CustomerID == 0 || cmd.PlatformUserID == "" || len(cmd.PlatformUserID) > 128 ||
		cmd.ActorID == "" || len(cmd.ActorID) > 128 || cmd.Reason == "" || len([]rune(cmd.Reason)) > 500 || cmd.IdempotencyKey == "" || len(cmd.IdempotencyKey) > 128 {
		return DisableResult{}, ErrInvalidClaims
	}
	repository, ok := s.repo.(AccessDisableRepository)
	if !ok {
		return DisableResult{}, ErrIdentityDisabled
	}
	return repository.DisableLink(ctx, cmd, s.clock.Now().UTC())
}

func disableRequestHash(command DisableCommand) string {
	sum := sha256.Sum256([]byte(command.TenantID + "\x00" + command.PlatformUserID + "\x00" + strconv.FormatUint(command.CustomerID, 10) + "\x00" + command.Reason))
	return hex.EncodeToString(sum[:])
}

func (s *Service) Provision(ctx context.Context, cmd ProvisionCommand) (ProvisionResult, error) {
	// 预配只建立待激活映射，不等价于登录成功；真实 OIDC subject 仍需在回调阶段匹配。
	if strings.TrimSpace(cmd.TenantID) == "" || strings.TrimSpace(cmd.AccountNo) == "" || strings.TrimSpace(cmd.PlatformUserID) == "" || cmd.CustomerID == 0 {
		return ProvisionResult{}, ErrInvalidClaims
	}
	link, err := s.repo.UpsertPendingLink(ctx, &IdentityLink{Model: newModel(cmd.TenantID, cmd.PlatformUserID, s.clock.Now()), AccountNo: cmd.AccountNo, PlatformUserID: cmd.PlatformUserID, CustomerID: cmd.CustomerID, ContactID: cmd.ContactID, Status: IdentityPending, DisplayName: cmd.DisplayName})
	if err != nil {
		return ProvisionResult{}, err
	}
	return ProvisionResult{PortalAccountID: portalAccountID(link.ID), AccountNo: link.AccountNo}, nil
}

func portalAccountID(value uint64) string {
	return "PA" + strconv.FormatUint(value, 10)
}

type LoginStart struct{ AuthorizationURL, State string }

func (s *Service) BeginInvitationLogin(ctx context.Context, inviteToken, returnPath string) (LoginStart, error) {
	verified, err := s.invites.Verify(ctx, inviteToken)
	if err != nil {
		return LoginStart{}, err
	}
	if s.usePlatformBinding {
		// Phase 5：本地映射退役。邀请登录的客户边界在 OIDC 回调时由平台 customer_ref
		// 与邀请的 CustomerID 双重匹配，不再查 portal_identity_links。
		return s.begin(ctx, verified.TenantID, verified.ExpectedPlatformUserID, verified.CustomerID, inviteToken, returnPath)
	}
	link, err := s.repo.FindLink(ctx, verified.TenantID, verified.ExpectedPlatformUserID)
	if err != nil || link.CustomerID != verified.CustomerID || link.Status == IdentityDisabled {
		return LoginStart{}, ErrNotProvisioned
	}
	return s.begin(ctx, verified.TenantID, verified.ExpectedPlatformUserID, verified.CustomerID, inviteToken, returnPath)
}

func (s *Service) BeginLogin(ctx context.Context, tenantID, returnPath string) (LoginStart, error) {
	if strings.TrimSpace(tenantID) == "" {
		return LoginStart{}, ErrInvalidClaims
	}
	// subject 只有在 OIDC 回调验签后才可信。普通登录先建未绑定上下文，再按已验证 sub 查活动映射；邀请登录则预先绑定邀请对象。
	return s.begin(ctx, tenantID, "", 0, "", returnPath)
}

func (s *Service) begin(ctx context.Context, tenantID, subject string, customerID uint64, inviteToken, returnPath string) (LoginStart, error) {
	if !safeReturnPath(returnPath) {
		returnPath = "/"
	}
	state, err := s.token(32)
	if err != nil {
		return LoginStart{}, err
	}
	nonce, err := s.token(32)
	if err != nil {
		return LoginStart{}, err
	}
	verifier, err := s.token(64)
	if err != nil {
		return LoginStart{}, err
	}
	cipher, err := s.protector.Encrypt(ctx, []byte(verifier))
	if err != nil {
		return LoginStart{}, err
	}
	nonceCipher, err := s.protector.Encrypt(ctx, []byte(nonce))
	if err != nil {
		return LoginStart{}, err
	}
	now := s.clock.Now().UTC()
	var inviteCipher []byte
	if inviteToken != "" {
		inviteCipher, err = s.protector.Encrypt(ctx, []byte(inviteToken))
		if err != nil {
			return LoginStart{}, err
		}
	}
	actorID := subject
	if actorID == "" {
		actorID = "portal-login"
	}
	activation := &ActivationContext{Model: newModel(tenantID, actorID, now), ContextIDHash: hash(state + ":" + nonce), InviteTokenHash: hash(inviteToken), InviteTokenCipher: inviteCipher, ExpectedPlatformUserID: subject, CustomerID: customerID, StateHash: hash(state), NonceHash: hash(nonce), NonceCipher: nonceCipher, PKCEVerifierCipher: cipher, ReturnPath: returnPath, ExpiresAt: now.Add(10 * time.Minute)}
	if err = s.repo.CreateActivation(ctx, activation); err != nil {
		return LoginStart{}, err
	}
	challengeSum := sha256.Sum256([]byte(verifier))
	url, err := s.oidc.AuthorizationURL(state, nonce, base64.RawURLEncoding.EncodeToString(challengeSum[:]), returnPath)
	if err != nil {
		return LoginStart{}, err
	}
	return LoginStart{AuthorizationURL: url, State: state}, nil
}

type LoginResult struct {
	SessionToken string
	CustomerID   uint64
	ExpiresAt    time.Time
	ReturnPath   string
}

type LoginMetadata struct {
	IPHash, IPMasked, Location, Device, UserAgentHash string
}

func (s *Service) CompleteLogin(ctx context.Context, state, code string, metadata LoginMetadata) (LoginResult, error) {
	// state 先按摘要一次性消费，再解密 nonce/PKCE 完成令牌校验，防止回调重放与授权码替换。
	now := s.clock.Now().UTC()
	activation, err := s.repo.ConsumeActivation(ctx, hash(state), now)
	if err != nil {
		return LoginResult{}, ErrInvalidLoginState
	}
	nonce, err := s.protector.Decrypt(ctx, activation.NonceCipher)
	if err != nil || activation.NonceHash != hash(string(nonce)) {
		return LoginResult{}, ErrInvalidLoginState
	}
	verifier, err := s.protector.Decrypt(ctx, activation.PKCEVerifierCipher)
	if err != nil {
		return LoginResult{}, err
	}
	claims, err := s.oidc.ExchangeAndValidate(ctx, code, string(verifier), string(nonce))
	if err != nil {
		s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "MEDIUM", "OIDC_EXCHANGE_REJECTED", metadata, now)
		if errors.Is(err, sharedauthorization.ErrForbidden) {
			return LoginResult{}, ErrPortalAuthorization
		}
		if errors.Is(err, sharedauthorization.ErrUnavailable) {
			return LoginResult{}, ErrAuthorizationUnavailable
		}
		return LoginResult{}, ErrInvalidClaims
	}
	if claims.IdentityID == "" {
		// Compatibility for in-process legacy adapters; the production OIDC
		// adapter always requires identity_id in the ID token/context.
		claims.IdentityID = claims.Subject
	}
	if activation.ExpectedPlatformUserID != "" && claims.IdentityID != activation.ExpectedPlatformUserID {
		s.writeLoginSecurityEvent(ctx, activation, "SUBJECT_MISMATCH", "HIGH", "OIDC_SUBJECT_MISMATCH", metadata, now)
		return LoginResult{}, ErrSubjectMismatch
	}
	if claims.TenantID != activation.TenantID {
		s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_TENANT_MISMATCH", metadata, now)
		return LoginResult{}, ErrInvalidClaims
	}
	if claims.RoleConfigHash != s.roleConfigHash {
		s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_ROLE_CONFIG_MISMATCH", metadata, now)
		return LoginResult{}, ErrInvalidClaims
	}
	if !validPortalAuthorization(claims, s.environmentCode) {
		s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_PORTAL_AUTHORIZATION_REJECTED", metadata, now)
		return LoginResult{}, ErrPortalAuthorization
	}
	if !claims.ExpiresAt.After(now) || strings.TrimSpace(claims.AccessToken) == "" {
		s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_TOKEN_INVALID", metadata, now)
		return LoginResult{}, ErrInvalidClaims
	}
	// Phase 4：平台绑定开启且声明存在时，非邀请登录直接以 customer_ref 作为客户边界，
	// 不查本地映射；邀请登录仍走本地映射消费（映射激活由邀请链路负责）。声明缺失或
	// 开关关闭时回退旧路径（本地 portal_identity_links 仍为权威）。
	customerID := uint64(0)
	var link *IdentityLink
	if s.usePlatformBinding && claims.CustomerRef != "" {
		parsed, parseErr := strconv.ParseUint(claims.CustomerRef, 10, 64)
		if parseErr != nil || parsed == 0 {
			s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_CUSTOMER_REF_INVALID", metadata, now)
			return LoginResult{}, ErrNotProvisioned
		}
		customerID = parsed
		// 双来源一致性：过渡期本地映射仍存在时：
		//   - 映射已被管理端禁用 → 失败关闭（禁用 saga 未收敛到平台时不得放行新登录）；
		//   - 客户与平台 customer_ref 不一致 → 记安全事件并失败关闭。
		if local, localErr := s.repo.FindLink(ctx, claims.TenantID, claims.IdentityID); localErr == nil {
			if local.Status == IdentityDisabled {
				s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_LINK_DISABLED", metadata, now)
				return LoginResult{}, ErrNotProvisioned
			}
			if local.CustomerID != customerID {
				s.writeLoginSecurityEvent(ctx, activation, "LOGIN_FAILED", "HIGH", "OIDC_CUSTOMER_REF_LINK_MISMATCH", metadata, now)
				return LoginResult{}, ErrSubjectMismatch
			}
		}
		if activation.ExpectedPlatformUserID != "" {
			// Phase 5：邀请路径的客户边界 = 平台 customer_ref 与邀请 CustomerID 双匹配；
			// 不再依赖本地映射，邀请消费仍由 CRM 侧收敛。
			if activation.CustomerID != 0 && activation.CustomerID != customerID {
				s.writeLoginSecurityEvent(ctx, activation, "SUBJECT_MISMATCH", "HIGH", "OIDC_CUSTOMER_REF_MISMATCH", metadata, now)
				return LoginResult{}, ErrSubjectMismatch
			}
		}
	} else {
		found, linkErr := s.repo.FindLink(ctx, claims.TenantID, claims.IdentityID)
		if linkErr != nil || found.Status == IdentityDisabled || (activation.CustomerID != 0 && found.CustomerID != activation.CustomerID) {
			return LoginResult{}, ErrNotProvisioned
		}
		if activation.ExpectedPlatformUserID == "" && found.Status != IdentityActive {
			return LoginResult{}, ErrNotProvisioned
		}
		link = found
		customerID = link.CustomerID
	}
	var inviteToken string
	if activation.InviteTokenHash != hash("") {
		rawInviteToken, decryptErr := s.protector.Decrypt(ctx, activation.InviteTokenCipher)
		if decryptErr != nil {
			return LoginResult{}, decryptErr
		}
		inviteToken = string(rawInviteToken)
	}
	rawSession, err := s.token(48)
	if err != nil {
		return LoginResult{}, err
	}
	expires := claims.ExpiresAt
	if capAt := now.Add(s.maxSessionAge); expires.After(capAt) {
		expires = capAt
	}
	accessTokenCipher, err := s.protector.Encrypt(ctx, []byte(claims.AccessToken))
	if err != nil {
		return LoginResult{}, err
	}
	publicSessionID, err := s.token(24)
	if err != nil {
		return LoginResult{}, err
	}
	session := &Session{Model: newModel(claims.TenantID, claims.IdentityID, now), PublicID: publicSessionID, SessionIDHash: hash(rawSession), PlatformUserID: claims.IdentityID, CustomerID: customerID, AuthzRevision: claims.AuthzRevision, RoleConfigHash: claims.RoleConfigHash, Roles: append([]string(nil), claims.Roles...), Permissions: append([]string(nil), claims.Permissions...), DataScopes: cloneDataScopes(claims.DataScopes), AccessTokenCipher: accessTokenCipher, AuthorizationCheckedAt: now, ExpiresAt: expires, AbsoluteExpiry: expires, LastSeenAt: now, IPHash: metadata.IPHash, UserAgentHash: metadata.UserAgentHash, IPMasked: metadata.IPMasked, LocationSnapshot: metadata.Location, DeviceSnapshot: metadata.Device}
	successEvent, err := s.newSecurityEvent(claims.TenantID, claims.IdentityID, customerID, "LOGIN_SUCCEEDED", "LOW", "", metadata, now)
	if err != nil {
		return LoginResult{}, err
	}
	activatedNow := link != nil && link.Status == IdentityPending
	if err = s.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		if activatedNow {
			if activateErr := s.repo.ActivateLink(txCtx, claims.TenantID, link.ID, claims.AuthzRevision, claims.IdentityID, now); activateErr != nil {
				return activateErr
			}
		}
		if createErr := s.repo.CreateSession(txCtx, session); createErr != nil {
			return createErr
		}
		return s.repo.CreateSecurityEvent(txCtx, successEvent)
	}); err != nil {
		return LoginResult{}, err
	}
	// CRM 邀请消费故意放在最后：远端成功前浏览器会话已持久化；若消费失败则撤销会话并保留邀请可重试。
	if inviteToken != "" {
		if err = s.invites.Consume(ctx, inviteToken, claims.IdentityID); err != nil {
			revokeErr := s.repo.RevokeSession(ctx, claims.TenantID, claims.IdentityID, session.SessionIDHash, now)
			var revertErr error
			if activatedNow {
				revertErr = s.repo.RevertActivation(ctx, claims.TenantID, link.ID, claims.AuthzRevision, claims.IdentityID, now)
			}
			return LoginResult{}, errors.Join(err, revokeErr, revertErr)
		}
	}
	return LoginResult{SessionToken: rawSession, CustomerID: customerID, ExpiresAt: expires, ReturnPath: activation.ReturnPath}, nil
}

func validPortalAuthorization(claims Claims, environmentCode string) bool {
	identityID := claims.IdentityID
	if identityID == "" {
		identityID = claims.Subject
	}
	manifest := platformcatalog.PortalManifest()
	if len(claims.Roles) != 1 || claims.Roles[0] != portalRole || !platformcatalog.HasRole(manifest, claims.Roles[0]) || len(claims.Permissions) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(claims.Permissions))
	for _, permission := range claims.Permissions {
		if permission == "all" || !platformcatalog.HasPermission(manifest, permission) || strings.TrimSpace(permission) != permission {
			return false
		}
		if _, duplicate := seen[permission]; duplicate {
			return false
		}
		seen[permission] = struct{}{}
	}
	_, decision, err := sharedauthorization.ValidateScopes(claims.DataScopes, claims.Roles, environmentCode, identityID, claims.PersonID)
	// Portal data is always bound to the server-side IdentityLink customer.
	// APPLICATION/TENANT/ENVIRONMENT and a matching SELF scope may authorize
	// that customer; ORG/PROJECT-only scopes cannot be safely translated to a
	// customer and therefore fail closed.
	return err == nil && (decision.AllowAll || len(decision.SelfIDs) > 0)
}

// AuthenticateSession 校验本地会话并在线复核授权。allowStale 仅对只读请求为 true：
// 授权服务不可用时可放行受控陈旧窗口；写请求必须在线复核（P1-3）。
func (s *Service) AuthenticateSession(ctx context.Context, tenantID, rawToken string, allowStale bool) (*Session, error) {
	// Cookie 仅携带高熵令牌，数据库按摘要检索；租户、撤销和到期边界均由服务端重新验证。
	now := s.clock.Now().UTC()
	sessionHash := hash(rawToken)
	session, err := s.repo.FindSession(ctx, tenantID, sessionHash, now)
	if err != nil {
		return nil, ErrInvalidLoginState
	}
	var link *IdentityLink
	if s.usePlatformBinding {
		// 平台绑定路径：会话客户边界由登录时的 customer_ref 建立，本地映射不再是权威。
		if session.CustomerID == 0 {
			return nil, ErrIdentityDisabled
		}
	} else {
		found, linkErr := s.repo.FindLink(ctx, tenantID, session.PlatformUserID)
		if linkErr != nil || found.Status != IdentityActive || found.CustomerID != session.CustomerID {
			return nil, ErrIdentityDisabled
		}
		link = found
	}
	checkedAt := time.Time{}
	if now.Sub(session.AuthorizationCheckedAt) >= authorizationCheckInterval {
		accessToken, decryptErr := s.protector.Decrypt(ctx, session.AccessTokenCipher)
		if decryptErr != nil {
			s.revokeAuthorization(ctx, tenantID, session.PlatformUserID, now, "ACCESS_TOKEN_INVALID")
			return nil, ErrInvalidLoginState
		}
		current, currentErr := s.oidc.UserInfo(ctx, string(accessToken))
		if errors.Is(currentErr, sharedauthorization.ErrUnavailable) {
			// P1-3：陈旧授权只放行只读方法；写请求与超窗请求必须在线复核。
			if !allowStale || now.Sub(session.AuthorizationCheckedAt) > authorizationMaxStale {
				return nil, ErrAuthorizationUnavailable
			}
			if err = s.repo.TouchSession(ctx, tenantID, sessionHash, now, time.Time{}); err != nil {
				return nil, ErrInvalidLoginState
			}
			session.LastSeenAt = now
			return session, nil
		}
		if currentErr != nil || !samePortalAuthorization(session, current, tenantID, s.roleConfigHash, s.environmentCode) {
			s.revokeAuthorization(ctx, tenantID, session.PlatformUserID, now, "USERINFO_REJECTED")
			return nil, ErrInvalidLoginState
		}
		if s.usePlatformBinding {
			// 客户重绑定必须即时生效：customer_ref 与登录时建立的边界不一致即撤销会话。
			parsed, parseErr := strconv.ParseUint(current.CustomerRef, 10, 64)
			if parseErr != nil || parsed != session.CustomerID {
				s.revokeAuthorization(ctx, tenantID, session.PlatformUserID, now, "CUSTOMER_REF_CHANGED")
				return nil, ErrInvalidLoginState
			}
		}
		checkedAt = now
		if link != nil {
			if err = s.repo.MarkLinkVerified(ctx, tenantID, link.ID, current.AuthzRevision, now); err != nil {
				s.revokeAuthorization(ctx, tenantID, session.PlatformUserID, now, "IDENTITY_LINK_INVALID")
				return nil, ErrInvalidLoginState
			}
		}
		session.AuthorizationCheckedAt = now
	}
	if err = s.repo.TouchSession(ctx, tenantID, sessionHash, now, checkedAt); err != nil {
		return nil, ErrInvalidLoginState
	}
	session.LastSeenAt = now
	return session, nil
}

func (s *Service) revokeAuthorization(ctx context.Context, tenantID, subject string, now time.Time, reason string) {
	_ = s.repo.RevokeSessionsForSubject(ctx, tenantID, subject, now)
	requestID := requestctx.ID(ctx)
	if requestID == "" {
		requestID = requestctx.NewID()
	}
	_ = s.repo.WriteAuthEvent(ctx, &AuthEvent{TenantID: tenantID, PlatformUserID: subject, Type: "USERINFO_VERIFY", Result: "REJECTED", ReasonCode: reason, RequestID: requestID, OccurredAt: now})
	if link, err := s.repo.FindLink(ctx, tenantID, subject); err == nil {
		s.writeSecurityEvent(ctx, tenantID, subject, link.CustomerID, "AUTHORIZATION_INVALIDATED", "HIGH", reason, LoginMetadata{}, now)
	}
}

func samePortalAuthorization(session *Session, current Claims, tenantID, roleConfigHash, environmentCode string) bool {
	return current.IdentityID == session.PlatformUserID && current.TenantID == tenantID && current.RoleConfigHash == roleConfigHash &&
		current.AuthzRevision == session.AuthzRevision && validPortalAuthorization(current, environmentCode) &&
		sameStringSet(session.Roles, current.Roles) && sameStringSet(session.Permissions, current.Permissions) &&
		sameDataScopeSet(session.DataScopes, current.DataScopes)
}

func cloneDataScopes(scopes []DataScope) []DataScope {
	return append([]DataScope(nil), scopes...)
}

func sameDataScopeSet(left, right []DataScope) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[DataScope]int, len(left))
	for _, scope := range left {
		seen[scope]++
	}
	for _, scope := range right {
		if seen[scope] == 0 {
			return false
		}
		seen[scope]--
	}
	return true
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

// Logout 先撤销服务端会话，再由浏览器清除 Cookie；仅删除 Cookie 无法阻止已泄露令牌继续使用。
func (s *Service) Logout(ctx context.Context, tenantID, subject, rawToken string) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(subject) == "" || strings.TrimSpace(rawToken) == "" {
		return ErrInvalidLoginState
	}
	return s.repo.RevokeSession(ctx, tenantID, subject, hash(rawToken), s.clock.Now().UTC())
}

type SecuritySession struct {
	ID         string    `json:"id"`
	Current    bool      `json:"current"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IPMasked   string    `json:"ip_masked,omitempty"`
	Location   string    `json:"location,omitempty"`
	Device     string    `json:"device,omitempty"`
}

type SecurityEventView struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	RiskLevel      string     `json:"risk_level"`
	IPMasked       string     `json:"ip_masked,omitempty"`
	Location       string     `json:"location,omitempty"`
	Device         string     `json:"device,omitempty"`
	ReasonCode     string     `json:"reason_code,omitempty"`
	OccurredAt     time.Time  `json:"occurred_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type SecurityOverview struct {
	AccountIdentifier string              `json:"account_identifier"`
	LastPortalLoginAt *time.Time          `json:"last_portal_login_at,omitempty"`
	LastIPMasked      string              `json:"last_ip_masked,omitempty"`
	LastLocation      string              `json:"last_location,omitempty"`
	LastDevice        string              `json:"last_device,omitempty"`
	SecurityCenterURL string              `json:"security_center_url"`
	Events            []SecurityEventView `json:"events"`
}

// AccountSecurity 只返回已认证 OIDC 主体拥有的数据。
// 安全中心地址来自受控配置而非请求参数，避免攻击者注入跳转目标。
func (s *Service) AccountSecurity(ctx context.Context, session *Session, securityCenterURL string) (SecurityOverview, error) {
	events, err := s.repo.ListSecurityEvents(ctx, session.TenantID, session.PlatformUserID, 50)
	if err != nil {
		return SecurityOverview{}, err
	}
	views := securityEventViews(events)
	result := SecurityOverview{AccountIdentifier: maskAccountIdentifier(session.PlatformUserID), SecurityCenterURL: securityCenterURL, Events: views}
	for i := range events {
		if events[i].Type == "LOGIN_SUCCEEDED" {
			result.LastPortalLoginAt = &events[i].OccurredAt
			result.LastIPMasked = events[i].IPMasked
			result.LastLocation = events[i].LocationSnapshot
			result.LastDevice = events[i].DeviceSnapshot
			break
		}
	}
	return result, nil
}

func (s *Service) Sessions(ctx context.Context, session *Session) ([]SecuritySession, error) {
	now := s.clock.Now().UTC()
	values, err := s.repo.ListSessions(ctx, session.TenantID, session.PlatformUserID, now)
	if err != nil {
		return nil, err
	}
	result := make([]SecuritySession, 0, len(values))
	for i := range values {
		result = append(result, SecuritySession{ID: values[i].PublicID, Current: values[i].SessionIDHash == session.SessionIDHash, CreatedAt: values[i].CreatedAt, LastSeenAt: values[i].LastSeenAt, ExpiresAt: values[i].ExpiresAt, IPMasked: values[i].IPMasked, Location: values[i].LocationSnapshot, Device: values[i].DeviceSnapshot})
	}
	return result, nil
}

func (s *Service) RevokeOwnedSession(ctx context.Context, session *Session, publicID string) (bool, error) {
	if strings.TrimSpace(publicID) == "" {
		return false, ErrSessionNotFound
	}
	now := s.clock.Now().UTC()
	owned, err := s.repo.FindOwnedSession(ctx, session.TenantID, session.PlatformUserID, publicID, now)
	if err != nil {
		return false, err
	}
	if err = s.repo.RevokeSession(ctx, session.TenantID, session.PlatformUserID, owned.SessionIDHash, now); err != nil {
		return false, err
	}
	s.writeSecurityEvent(ctx, session.TenantID, session.PlatformUserID, session.CustomerID, "SESSION_REVOKED", "LOW", "USER_REQUEST", LoginMetadata{IPMasked: owned.IPMasked, Location: owned.LocationSnapshot, Device: owned.DeviceSnapshot}, now)
	return owned.SessionIDHash == session.SessionIDHash, nil
}

func (s *Service) AcknowledgeSecurityEvent(ctx context.Context, session *Session, publicID string) error {
	if strings.TrimSpace(publicID) == "" {
		return ErrSecurityEventNotFound
	}
	return s.repo.AcknowledgeSecurityEvent(ctx, session.TenantID, session.PlatformUserID, publicID, s.clock.Now().UTC())
}

func securityEventViews(values []SecurityEvent) []SecurityEventView {
	result := make([]SecurityEventView, 0, len(values))
	for i := range values {
		result = append(result, SecurityEventView{ID: values[i].PublicID, Type: values[i].Type, RiskLevel: values[i].RiskLevel, IPMasked: values[i].IPMasked, Location: values[i].LocationSnapshot, Device: values[i].DeviceSnapshot, ReasonCode: values[i].ReasonCode, OccurredAt: values[i].OccurredAt, AcknowledgedAt: values[i].AcknowledgedAt})
	}
	return result
}

func (s *Service) writeLoginSecurityEvent(ctx context.Context, activation *ActivationContext, eventType, risk, reason string, metadata LoginMetadata, now time.Time) {
	if activation.ExpectedPlatformUserID == "" || activation.CustomerID == 0 {
		// 普通登录失败时尚无已认证主体，不能把事件归到猜测账号，否则会形成可被 IDOR 利用的错误归属。
		return
	}
	s.writeSecurityEvent(ctx, activation.TenantID, activation.ExpectedPlatformUserID, activation.CustomerID, eventType, risk, reason, metadata, now)
}

func (s *Service) writeSecurityEvent(ctx context.Context, tenantID, subject string, customerID uint64, eventType, risk, reason string, metadata LoginMetadata, now time.Time) {
	event, err := s.newSecurityEvent(tenantID, subject, customerID, eventType, risk, reason, metadata, now)
	if err == nil {
		_ = s.repo.CreateSecurityEvent(ctx, event)
	}
}

func (s *Service) newSecurityEvent(tenantID, subject string, customerID uint64, eventType, risk, reason string, metadata LoginMetadata, now time.Time) (*SecurityEvent, error) {
	publicID, err := s.token(24)
	if err != nil {
		return nil, err
	}
	return &SecurityEvent{Model: newModel(tenantID, subject, now), PublicID: publicID, PlatformUserID: subject, CustomerID: customerID, Type: eventType, RiskLevel: risk, IPHash: metadata.IPHash, IPMasked: metadata.IPMasked, LocationSnapshot: metadata.Location, DeviceSnapshot: metadata.Device, ReasonCode: reason, OccurredAt: now}, nil
}

func maskAccountIdentifier(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:2]) + strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-2:])
}

func (s *Service) token(size int) (string, error) {
	b, err := s.random.Bytes(size)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func safeReturnPath(path string) bool {
	// 回跳地址只能是站内绝对路径，并拒绝协议相对地址，防止登录成功后发生开放重定向。
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.ContainsAny(path, "\r\n")
}

func newModel(tenantID, actorID string, now time.Time) database.Model {
	return database.Model{TenantID: tenantID, CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now, Version: 1}
}
