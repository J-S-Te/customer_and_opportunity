package crmauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"golang.org/x/oauth2"
)

const (
	loginTTL                   = 10 * time.Minute
	authorizationCheckInterval = 15 * time.Second
)

var ErrUnauthenticated = errors.New("CRM session is not authenticated")

type Options struct {
	TenantID, RoleConfigHash string
	SessionTTL               time.Duration
	MaxRoles                 int
}

type Service struct {
	repo    repository
	oidc    oidcClient
	codec   *secretCodec
	options Options
	now     func() time.Time
}

func NewService(repo repository, oidc oidcClient, encryptionKey []byte, options Options) (*Service, error) {
	codec, err := newSecretCodec(encryptionKey)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, oidc: oidc, codec: codec, options: options, now: time.Now}, nil
}

type LoginStart struct{ AuthorizationURL string }

func (s *Service) BeginLogin(ctx context.Context, returnPath string) (LoginStart, error) {
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
	if err := s.repo.SaveLogin(ctx, &LoginTransaction{StateHash: tokenHash(state), TenantID: s.options.TenantID, NonceCipher: nonceCipher, CodeVerifierCipher: verifierCipher, ReturnPath: returnPath, ExpiresAt: now.Add(loginTTL), CreatedAt: now}); err != nil {
		return LoginStart{}, err
	}
	return LoginStart{AuthorizationURL: s.oidc.AuthorizationURL(state, nonce, verifier)}, nil
}

type LoginResult struct {
	SessionToken, ReturnPath string
	ExpiresAt                time.Time
}

func (s *Service) CompleteLogin(ctx context.Context, state, code string) (LoginResult, error) {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(code) == "" {
		return LoginResult{}, ErrUnauthenticated
	}
	now := s.now().UTC()
	transaction, err := s.repo.ConsumeLogin(ctx, tokenHash(state), now)
	if err != nil {
		return LoginResult{}, ErrUnauthenticated
	}
	nonce, err := s.codec.decrypt(transaction.NonceCipher)
	if err != nil {
		return LoginResult{}, ErrUnauthenticated
	}
	verifier, err := s.codec.decrypt(transaction.CodeVerifierCipher)
	if err != nil {
		return LoginResult{}, ErrUnauthenticated
	}
	claims, err := s.oidc.Exchange(ctx, code, verifier, nonce)
	if err != nil {
		return LoginResult{}, ErrUnauthenticated
	}
	claims, err = normalizeAuthorization(claims, s.options.TenantID, s.options.RoleConfigHash, s.options.MaxRoles)
	if err != nil || !claims.ExpiresAt.After(now) {
		return LoginResult{}, ErrUnauthenticated
	}
	expiresAt := earliestExpiry(claims.ExpiresAt, now.Add(s.options.SessionTTL))
	if !expiresAt.After(now) {
		return LoginResult{}, ErrUnauthenticated
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
	organizationIDsJSON, err := json.Marshal(claims.OrganizationIDs)
	if err != nil {
		return LoginResult{}, err
	}
	accessTokenCipher, err := s.codec.encrypt(claims.AccessToken)
	if err != nil {
		return LoginResult{}, err
	}
	session := &Session{SessionIDHash: tokenHash(rawSession), TenantID: claims.TenantID, PlatformUserID: claims.Subject, PersonID: claims.PersonID, DisplayName: claims.DisplayName, PrimaryOrgID: claims.PrimaryOrgID, OrganizationIDsJSON: organizationIDsJSON, RolesJSON: rolesJSON, PermissionsJSON: permissionsJSON, RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision, AccessTokenCipher: accessTokenCipher, ExpiresAt: expiresAt, AuthorizationCheckedAt: now, CreatedAt: now, LastSeenAt: now}
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
	if now.Sub(session.AuthorizationCheckedAt) >= authorizationCheckInterval {
		accessToken, decryptErr := s.codec.decrypt(session.AccessTokenCipher)
		if decryptErr != nil {
			_ = s.repo.RevokeSession(ctx, session.SessionIDHash, now)
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		current, currentErr := s.oidc.UserInfo(ctx, accessToken)
		if currentErr != nil {
			_ = s.repo.RevokeSession(ctx, session.SessionIDHash, now)
			return sharedauth.Principal{}, ErrUnauthenticated
		}
		current, currentErr = normalizeAuthorization(current, s.options.TenantID, s.options.RoleConfigHash, s.options.MaxRoles)
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
	if err := json.Unmarshal(session.OrganizationIDsJSON, &organizationIDs); err != nil {
		return verifiedClaims{}, err
	}
	if err := json.Unmarshal(session.RolesJSON, &roles); err != nil {
		return verifiedClaims{}, err
	}
	if err := json.Unmarshal(session.PermissionsJSON, &permissions); err != nil {
		return verifiedClaims{}, err
	}
	return verifiedClaims{Subject: session.PlatformUserID, TenantID: session.TenantID, PersonID: session.PersonID, DisplayName: session.DisplayName, PrimaryOrgID: session.PrimaryOrgID, OrganizationIDs: organizationIDs, Roles: roles, Permissions: permissions, RoleConfigHash: session.RoleConfigHash, AuthzRevision: session.AuthzRevision}, nil
}

func principalFromClaims(claims verifiedClaims) sharedauth.Principal {
	permissions := make(map[string]struct{}, len(claims.Permissions))
	for _, permission := range claims.Permissions {
		permissions[permission] = struct{}{}
	}
	scope := scopeForRoles(claims.Roles)
	// PersonID comes only from the platform's explicit tenant-scoped PMS binding.
	// It remains empty when no binding exists and is never inferred from sub.
	return sharedauth.Principal{UserID: claims.Subject, PersonID: claims.PersonID, TenantID: claims.TenantID, DisplayName: claims.DisplayName, PrimaryOrgID: claims.PrimaryOrgID, Roles: append([]string(nil), claims.Roles...), Permissions: permissions, ScopeMode: scope, OrganizationIDs: append([]string(nil), claims.OrganizationIDs...), RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision}
}

func scopeForRoles(roles []string) sharedauth.ScopeMode {
	for _, role := range roles {
		switch role {
		case "sales_director", "team_lead", "technical_lead", "customer_admin", "auditor":
			return sharedauth.ScopeAll
		}
	}
	return sharedauth.ScopeSelf
}

func safeReturnPath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.ContainsAny(path, "\r\n")
}
