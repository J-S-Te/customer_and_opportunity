package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

type securityRepository struct {
	sessions       []Session
	events         []SecurityEvent
	owned          *Session
	revokedHash    string
	ackSubject     string
	securityWrites []SecurityEvent
}

func (r *securityRepository) WithTransaction(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (*securityRepository) UpsertPendingLink(context.Context, *IdentityLink) (*IdentityLink, error) {
	return nil, errors.New("not used")
}
func (*securityRepository) FindLink(context.Context, string, string) (*IdentityLink, error) {
	return nil, errors.New("not used")
}
func (*securityRepository) ActivateLink(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*securityRepository) RevertActivation(context.Context, string, uint64, uint64, string, time.Time) error {
	return errors.New("not used")
}
func (*securityRepository) CreateActivation(context.Context, *ActivationContext) error {
	return errors.New("not used")
}
func (*securityRepository) ConsumeActivation(context.Context, string, time.Time) (*ActivationContext, error) {
	return nil, errors.New("not used")
}
func (*securityRepository) CreateSession(context.Context, *Session) error {
	return errors.New("not used")
}
func (*securityRepository) FindSession(context.Context, string, string, time.Time) (*Session, error) {
	return nil, errors.New("not used")
}
func (r *securityRepository) ListSessions(_ context.Context, tenantID, subject string, _ time.Time) ([]Session, error) {
	if tenantID != "tenant-a" || subject != "subject-a" {
		return nil, ErrSessionNotFound
	}
	return r.sessions, nil
}
func (r *securityRepository) FindOwnedSession(_ context.Context, tenantID, subject, publicID string, _ time.Time) (*Session, error) {
	if tenantID != "tenant-a" || subject != "subject-a" || r.owned == nil || r.owned.PublicID != publicID {
		return nil, ErrSessionNotFound
	}
	return r.owned, nil
}
func (r *securityRepository) RevokeSession(_ context.Context, tenantID, subject, sessionHash string, _ time.Time) error {
	if tenantID != "tenant-a" || subject != "subject-a" {
		return ErrSessionNotFound
	}
	r.revokedHash = sessionHash
	return nil
}
func (*securityRepository) RevokeSessionsForSubject(context.Context, string, string, time.Time) error {
	return errors.New("not used")
}
func (*securityRepository) TouchSession(context.Context, string, string, time.Time, time.Time) error {
	return errors.New("not used")
}
func (*securityRepository) MarkLinkVerified(context.Context, string, uint64, uint64, time.Time) error {
	return errors.New("not used")
}
func (*securityRepository) WriteAuthEvent(context.Context, *AuthEvent) error {
	return errors.New("not used")
}
func (r *securityRepository) CreateSecurityEvent(_ context.Context, event *SecurityEvent) error {
	r.securityWrites = append(r.securityWrites, *event)
	return nil
}
func (r *securityRepository) ListSecurityEvents(_ context.Context, tenantID, subject string, _ int) ([]SecurityEvent, error) {
	if tenantID != "tenant-a" || subject != "subject-a" {
		return nil, ErrSecurityEventNotFound
	}
	return r.events, nil
}
func (r *securityRepository) AcknowledgeSecurityEvent(_ context.Context, tenantID, subject, publicID string, _ time.Time) error {
	if tenantID != "tenant-a" || subject != "subject-a" || publicID != "event-a" {
		return ErrSecurityEventNotFound
	}
	r.ackSubject = subject
	return nil
}

func securityService(repo Repository, now time.Time) *Service {
	return NewService(repo, nil, nil, nil, fixedClock{now: now}, deterministicRandom{}, "catalog", time.Minute)
}

type deterministicRandom struct{}

func (deterministicRandom) Bytes(size int) ([]byte, error) { return make([]byte, size), nil }

func TestSecurityViewsExposeOnlyOpaqueAndMaskedMetadata(t *testing.T) {
	now := time.Date(2026, 8, 1, 3, 4, 5, 0, time.UTC)
	repo := &securityRepository{
		sessions: []Session{{Model: database.Model{CreatedAt: now.Add(-time.Minute)}, PublicID: "public-a", SessionIDHash: "secret-hash", LastSeenAt: now, ExpiresAt: now.Add(time.Minute), IPHash: "secret-ip-hash", IPMasked: "192.0.0.0", DeviceSnapshot: "macOS · Safari"}},
		events:   []SecurityEvent{{PublicID: "event-a", PlatformUserID: "subject-a", Type: "LOGIN_SUCCEEDED", RiskLevel: "LOW", IPHash: "secret-ip-hash", IPMasked: "192.0.0.0", OccurredAt: now}},
	}
	session := &Session{Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: "secret-hash", PlatformUserID: "subject-a", CustomerID: 7}
	sessions, err := securityService(repo, now).Sessions(context.Background(), session)
	if err != nil || len(sessions) != 1 || sessions[0].ID != "public-a" || !sessions[0].Current || sessions[0].IPMasked != "192.0.0.0" {
		t.Fatalf("Sessions()=%#v err=%v", sessions, err)
	}
	overview, err := securityService(repo, now).AccountSecurity(context.Background(), session, "https://identity.example/security")
	if err != nil || overview.AccountIdentifier != "su*****-a" || len(overview.Events) != 1 || overview.Events[0].ID != "event-a" {
		t.Fatalf("AccountSecurity()=%#v err=%v", overview, err)
	}
}

func TestRevokeAndAcknowledgeAreSubjectScoped(t *testing.T) {
	now := time.Date(2026, 8, 1, 3, 4, 5, 0, time.UTC)
	repo := &securityRepository{owned: &Session{PublicID: "public-a", SessionIDHash: "secret-hash", IPMasked: "192.0.0.0"}}
	service := securityService(repo, now)
	session := &Session{Model: database.Model{TenantID: "tenant-a"}, SessionIDHash: "current-hash", PlatformUserID: "subject-a", CustomerID: 7}
	if _, err := service.RevokeOwnedSession(context.Background(), session, "public-b"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-account session error=%v", err)
	}
	current, err := service.RevokeOwnedSession(context.Background(), session, "public-a")
	if err != nil || current || repo.revokedHash != "secret-hash" || len(repo.securityWrites) != 1 {
		t.Fatalf("owned revoke err=%v hash=%q events=%d", err, repo.revokedHash, len(repo.securityWrites))
	}
	repo.owned.SessionIDHash = "current-hash"
	current, err = service.RevokeOwnedSession(context.Background(), session, "public-a")
	if err != nil || !current {
		t.Fatalf("current-session revoke current=%t err=%v", current, err)
	}
	if err := service.AcknowledgeSecurityEvent(context.Background(), session, "event-b"); !errors.Is(err, ErrSecurityEventNotFound) {
		t.Fatalf("cross-account ack error=%v", err)
	}
	if err := service.AcknowledgeSecurityEvent(context.Background(), session, "event-a"); err != nil || repo.ackSubject != "subject-a" {
		t.Fatalf("owned ack err=%v subject=%q", err, repo.ackSubject)
	}
}

func TestMaskAccountIdentifier(t *testing.T) {
	if got := maskAccountIdentifier("subject-a"); got != "su*****-a" {
		t.Fatalf("maskAccountIdentifier()=%q", got)
	}
}
