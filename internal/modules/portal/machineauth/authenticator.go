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

// Options 将 Portal 机器令牌绑定到基础平台发行方、部署级应用令牌受众以及一个确定租户。
type Options struct {
	Issuer, Audience, PublicKeyPath, TenantID string
}

// Authenticator 验证基础平台应用 JWT，并在返回机器主体前原子消费每个集成 nonce。
// “签名有效”不足以放行请求，时间戳和一次性 nonce 共同限制截获令牌的重放窗口。
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

// RequestReplay 仅属于 customer_portal 数据库，用唯一摘要在多副本间共享重放防线。
type RequestReplay struct {
	ReplayHash string `gorm:"primaryKey;size:64"`
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

func (RequestReplay) TableName() string { return "portal_machine_request_replays" }

// New 加载基础平台只读 Ed25519 应用令牌公钥；浏览器 OIDC discovery 属于另一适配器，此处不复用。
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

// Authenticate 拒绝浏览器令牌，并消费时间戳/nonce 组合；签名 scope 是返回主体的完整权限集。
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
	// 摘要绑定租户、客户端和主体，避免不同集成方复用同一个 nonce 相互占用或绕过审计边界。
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
	// 机器接口只接受 application token；主体、客户端和 scope 都要求规范形式，拒绝模糊等价值。
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
	// 唯一键承担跨进程原子消费；清理过期行只是控制表规模，不参与正确性判断。
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
