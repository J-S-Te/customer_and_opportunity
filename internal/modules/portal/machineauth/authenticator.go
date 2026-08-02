package machineauth

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/applicationjwt"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
)

const requestSkew = 5 * time.Minute

var noncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,200}$`)

// Options binds Portal machine tokens to the base platform's issuer,
// deployment-wide application-token audience, and one tenant.
type Options struct {
	Issuer, Audience, PublicKeyPath, TenantID string
}

// Authenticator verifies base-platform application JWTs and atomically
// consumes each integration nonce before returning a machine principal.
type Authenticator struct {
	verifier tokenVerifier
	replays  replayStore
	tenantID string
	now      func() time.Time
}

type tokenVerifier interface {
	Verify(context.Context, string) (claims, error)
}

type replayStore interface {
	Consume(context.Context, string, time.Time, time.Time) error
}

type claims struct {
	Subject, OAuthClientID, TenantID, TokenUse string
	Scopes                                     []string
	IssuedAt, NotBefore, ExpiresAt             time.Time
}

// RequestReplay belongs exclusively to the customer_portal schema.
type RequestReplay struct {
	ReplayHash string `gorm:"primaryKey;size:64"`
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

func (RequestReplay) TableName() string { return "portal_machine_request_replays" }

// New loads the base platform's read-only Ed25519 application-token public key.
// Browser OIDC discovery remains a separate adapter and is not used here.
func New(_ context.Context, db *gorm.DB, options Options) (*Authenticator, error) {
	verifier, err := applicationjwt.LoadVerifier(options.Issuer, options.Audience, options.PublicKeyPath)
	if err != nil {
		return nil, err
	}
	return newAuthenticator(&applicationTokenVerifier{verifier: verifier, now: time.Now}, &gormReplayStore{db: db}, options.TenantID, time.Now), nil
}

func newAuthenticator(verifier tokenVerifier, replays replayStore, tenantID string, now func() time.Time) *Authenticator {
	return &Authenticator{verifier: verifier, replays: replays, tenantID: tenantID, now: now}
}

// Authenticate rejects browser tokens and consumes the timestamp/nonce tuple.
// The signed scope claim is the returned principal's complete permission set.
func (a *Authenticator) Authenticate(ctx context.Context, request *http.Request) (sharedauth.Principal, error) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return sharedauth.Principal{}, errUnauthenticated
	}
	verified, err := a.verifier.Verify(ctx, parts[1])
	if err != nil {
		return sharedauth.Principal{}, errUnauthenticated
	}
	now := a.now().UTC()
	if err := validateClaims(verified, a.tenantID, now); err != nil {
		return sharedauth.Principal{}, errUnauthenticated
	}
	timestamp, err := time.Parse(time.RFC3339Nano, request.Header.Get("X-Integration-Timestamp"))
	nonce := strings.TrimSpace(request.Header.Get("X-Integration-Nonce"))
	if err != nil || !noncePattern.MatchString(nonce) || absoluteDuration(now.Sub(timestamp.UTC())) > requestSkew {
		return sharedauth.Principal{}, errUnauthenticated
	}
	replayHash := hashReplay(verified.TenantID, verified.OAuthClientID, verified.Subject, nonce)
	if err := a.replays.Consume(ctx, replayHash, now.Add(requestSkew), now); err != nil {
		if errors.Is(err, errReplay) {
			return sharedauth.Principal{}, errUnauthenticated
		}
		return sharedauth.Principal{}, err
	}
	permissions := make(map[string]struct{}, len(verified.Scopes))
	for _, scope := range verified.Scopes {
		permissions[scope] = struct{}{}
	}
	return sharedauth.Principal{UserID: "machine:" + verified.Subject, TenantID: verified.TenantID, Roles: []string{"machine"}, Permissions: permissions, ScopeMode: sharedauth.ScopeAll}, nil
}

func validateClaims(value claims, expectedTenant string, now time.Time) error {
	if value.TokenUse != "application" || value.TenantID != expectedTenant || strings.TrimSpace(value.Subject) == "" || value.Subject != strings.TrimSpace(value.Subject) || strings.TrimSpace(value.OAuthClientID) == "" || value.OAuthClientID != strings.TrimSpace(value.OAuthClientID) || len(value.Scopes) == 0 {
		return errUnauthenticated
	}
	if value.IssuedAt.IsZero() || value.NotBefore.IsZero() || value.ExpiresAt.IsZero() || value.NotBefore.Before(value.IssuedAt) || !value.ExpiresAt.After(value.NotBefore) || value.IssuedAt.After(now.Add(requestSkew)) || value.NotBefore.After(now.Add(requestSkew)) || !value.ExpiresAt.After(now) {
		return errUnauthenticated
	}
	seen := make(map[string]struct{}, len(value.Scopes))
	for _, scope := range value.Scopes {
		if !validScope(scope) {
			return errUnauthenticated
		}
		if _, duplicate := seen[scope]; duplicate {
			return errUnauthenticated
		}
		seen[scope] = struct{}{}
	}
	return nil
}

func validScope(value string) bool {
	return value == "portal.identity_mapping.provision" || value == "portal.identity_mapping.disable" || value == "report.callback.write" || value == "portal.report.risk.manage" || value == "portal.feedback.manage" || value == "portal.evaluation.read" || value == "portal.filing.unlock" || value == "portal.filing_material.scan.write" || value == "portal.project_message.manage" || value == "portal.project_history.read"
}

var (
	errUnauthenticated = errors.New("Portal machine request is not authenticated")
	errReplay          = errors.New("Portal machine request nonce was already consumed")
)

func absoluteDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

type applicationTokenVerifier struct {
	verifier *applicationjwt.Verifier
	now      func() time.Time
}

func (v *applicationTokenVerifier) Verify(_ context.Context, rawToken string) (claims, error) {
	verified, err := v.verifier.Verify(rawToken, v.now().UTC())
	if err != nil {
		return claims{}, err
	}
	return claims{
		Subject: verified.Subject, OAuthClientID: verified.OAuthClientID, TenantID: verified.TenantID, TokenUse: "application", Scopes: verified.Scopes,
		IssuedAt: verified.IssuedAt, NotBefore: verified.NotBefore, ExpiresAt: verified.ExpiresAt,
	}, nil
}

type gormReplayStore struct{ db *gorm.DB }

func (s *gormReplayStore) Consume(ctx context.Context, replayHash string, expiresAt, now time.Time) error {
	if err := s.db.WithContext(ctx).Where("expires_at <= ?", now).Delete(&RequestReplay{}).Error; err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Create(&RequestReplay{ReplayHash: replayHash, ExpiresAt: expiresAt, CreatedAt: now})
	if result.Error != nil && duplicateReplay(result.Error) {
		return errReplay
	}
	return result.Error
}

func duplicateReplay(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &mysqlErr) && mysqlErr.Number == 1062)
}
