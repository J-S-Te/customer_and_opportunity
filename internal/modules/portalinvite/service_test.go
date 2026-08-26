package portalinvite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

type fakeRepo struct {
	invites       []*Invite
	links         []*IdentityLink
	compensations []*CompensationTask
	consumeCalls  int
	operations    []*ProvisionOperation
}

func (r *fakeRepo) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	invites, links := clonePointers(r.invites), clonePointers(r.links)
	compensations, operations := clonePointers(r.compensations), clonePointers(r.operations)
	err := fn(ctx)
	if err != nil {
		r.invites, r.links = invites, links
		r.compensations, r.operations = compensations, operations
	}
	return err
}
func clonePointers[T any](values []*T) []*T {
	cloned := make([]*T, len(values))
	for index, value := range values {
		if value != nil {
			copyValue := *value
			cloned[index] = &copyValue
		}
	}
	return cloned
}
func (r *fakeRepo) LockCustomer(context.Context, string, uint64) error { return nil }
func (r *fakeRepo) RevokePending(_ context.Context, tenant string, customer uint64, actor, reason string, now time.Time) error {
	for _, value := range r.invites {
		if value.TenantID == tenant && value.CustomerID == customer && value.Status == StatusPending {
			value.Status, value.RevokedReason, value.RevokedAt = StatusRevoked, reason, &now
			value.Version++
		}
	}
	return nil
}
func (r *fakeRepo) CreateInvite(_ context.Context, value *Invite) error {
	value.ID = uint64(len(r.invites) + 1)
	r.invites = append(r.invites, value)
	return nil
}
func (r *fakeRepo) UpsertLink(_ context.Context, value *IdentityLink) error {
	value.ID = uint64(len(r.links) + 1)
	r.links = append(r.links, value)
	return nil
}
func (r *fakeRepo) FindCurrent(_ context.Context, tenant string, customer uint64) (*Invite, error) {
	for i := len(r.invites) - 1; i >= 0; i-- {
		if r.invites[i].TenantID == tenant && r.invites[i].CustomerID == customer {
			return r.invites[i], nil
		}
	}
	return nil, ErrNotFound
}
func (r *fakeRepo) FindByInviteNo(_ context.Context, tenant, no string) (*Invite, error) {
	for _, v := range r.invites {
		if v.TenantID == tenant && v.InviteNo == no {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *fakeRepo) FindByTokenHash(_ context.Context, hash string) (*Invite, error) {
	for _, v := range r.invites {
		if v.TokenHash == hash {
			return v, nil
		}
	}
	return nil, ErrNotFound
}
func (r *fakeRepo) FindByTokenHashForUpdate(ctx context.Context, hash string) (*Invite, error) {
	return r.FindByTokenHash(ctx, hash)
}
func (r *fakeRepo) FindIdentityLinkForInviteForUpdate(_ context.Context, invite *Invite) (*IdentityLink, error) {
	for _, value := range r.links {
		if value.TenantID == invite.TenantID && value.CustomerID == invite.CustomerID && value.ContactID == invite.ContactID &&
			value.PlatformUserID == invite.PlatformUserID && value.PortalAccountID == invite.PortalAccountID {
			return value, nil
		}
	}
	return nil, ErrVersionConflict
}
func (r *fakeRepo) ActivateIdentityLink(_ context.Context, invite *Invite, link *IdentityLink, _ string, now time.Time) error {
	if link.TenantID != invite.TenantID || link.CustomerID != invite.CustomerID || link.ContactID != invite.ContactID ||
		link.PlatformUserID != invite.PlatformUserID || link.PortalAccountID != invite.PortalAccountID || link.Status != StatusPending {
		return ErrVersionConflict
	}
	link.Status, link.LastVerifiedAt = "ACTIVE", &now
	link.Version++
	return nil
}
func (r *fakeRepo) MarkExpired(_ context.Context, id, version uint64, _ string, _ time.Time) error {
	for _, v := range r.invites {
		if v.ID == id && v.Version == version && v.Status == StatusPending {
			v.Status = StatusExpired
			v.Version++
			return nil
		}
	}
	return ErrVersionConflict
}
func (r *fakeRepo) Consume(_ context.Context, id, version uint64, subject, _ string, now time.Time) error {
	r.consumeCalls++
	for _, v := range r.invites {
		if v.ID == id && v.Version == version && v.Status == StatusPending && v.PlatformUserID == subject && v.ExpiresAt.After(now) {
			v.Status = StatusUsed
			v.UsedAt = &now
			v.Version++
			return nil
		}
	}
	return ErrVersionConflict
}
func (r *fakeRepo) Revoke(_ context.Context, id, version uint64, _, reason string, now time.Time) error {
	for _, v := range r.invites {
		if v.ID == id && v.Version == version && v.Status == StatusPending {
			v.Status = StatusRevoked
			v.RevokedReason = reason
			v.RevokedAt = &now
			v.Version++
			return nil
		}
	}
	return ErrVersionConflict
}
func (r *fakeRepo) CreateCompensation(_ context.Context, value *CompensationTask) error {
	for _, current := range r.compensations {
		if current.TenantID == value.TenantID && current.TaskNo == value.TaskNo {
			return nil
		}
	}
	r.compensations = append(r.compensations, value)
	return nil
}
func (r *fakeRepo) FindProvisionOperation(_ context.Context, tenant, actor, key string) (*ProvisionOperation, error) {
	for _, value := range r.operations {
		if value.TenantID == tenant && value.ActorID == actor && value.IdempotencyKey == key {
			return value, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *fakeRepo) FindProvisionOperationForUpdate(_ context.Context, tenant string, id uint64) (*ProvisionOperation, error) {
	for _, value := range r.operations {
		if value.TenantID == tenant && value.ID == id {
			return value, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *fakeRepo) CreateProvisionOperation(_ context.Context, value *ProvisionOperation) error {
	value.ID = uint64(len(r.operations) + 1)
	r.operations = append(r.operations, value)
	return nil
}
func (r *fakeRepo) AdvanceProvisionOperation(_ context.Context, value *ProvisionOperation, expected string, updates map[string]any, now time.Time) error {
	if value.Stage != expected {
		return ErrVersionConflict
	}
	value.Version++
	value.UpdatedAt = now
	if stage, ok := updates["stage"].(string); ok {
		value.Stage = stage
	}
	if status, ok := updates["status"].(string); ok {
		value.Status = status
	}
	if field, ok := updates["platform_user_id"].(string); ok {
		value.PlatformUserID = field
	}
	if field, ok := updates["account_no"].(string); ok {
		value.AccountNo = field
	}
	if field, ok := updates["portal_account_id"].(string); ok {
		value.PortalAccountID = field
	}
	if field, ok := updates["invite_id"].(uint64); ok {
		value.InviteID = &field
	}
	if field, ok := updates["token_cipher"].([]byte); ok {
		value.TokenCipher = field
	}
	return nil
}
func (r *fakeRepo) FindInviteByID(_ context.Context, tenant string, id uint64) (*Invite, error) {
	for _, value := range r.invites {
		if value.TenantID == tenant && value.ID == id {
			return value, nil
		}
	}
	return nil, ErrNotFound
}

type fakeCustomer struct {
	identity ContactIdentity
	err      error
}

func (f fakeCustomer) RegistrationContact(context.Context, auth.Principal, uint64) (ContactIdentity, error) {
	return f.identity, f.err
}

type fakePlatform struct {
	identity                  ProvisionedIdentity
	provisionErr, roleErr     error
	provisionCalls, roleCalls int
	roleKeys                  []string
}

func (f *fakePlatform) ProvisionExternalUser(context.Context, ContactIdentity) (ProvisionedIdentity, error) {
	f.provisionCalls++
	return f.identity, f.provisionErr
}
func (f *fakePlatform) AssignPortalRoleIdempotent(_ context.Context, _ string, key string) error {
	f.roleCalls++
	f.roleKeys = append(f.roleKeys, key)
	return f.roleErr
}

type fakePortal struct {
	mapping PortalMapping
	err     error
	calls   int
	keys    []string
}

func (f *fakePortal) ProvisionMappingIdempotent(_ context.Context, _ ContactIdentity, _ ProvisionedIdentity, key string) (PortalMapping, error) {
	f.calls++
	f.keys = append(f.keys, key)
	return f.mapping, f.err
}

type testOperationProtector struct{}

func (testOperationProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	result := make([]byte, base64.RawStdEncoding.EncodedLen(len(value)))
	base64.RawStdEncoding.Encode(result, value)
	return result, nil
}
func (testOperationProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	result := make([]byte, base64.RawStdEncoding.DecodedLen(len(value)))
	size, err := base64.RawStdEncoding.Decode(result, value)
	return result[:size], err
}

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type deterministicRandom struct{ next byte }

func (r *deterministicRandom) Bytes(size int) ([]byte, error) {
	r.next++
	return []byte(strings.Repeat(string(r.next), size)), nil
}

type fakeAudit struct {
	events []audit.Event
	err    error
}

func (a *fakeAudit) Write(_ context.Context, e audit.Event) error {
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, e)
	return nil
}

func serviceContext() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "sales-a", DisplayName: "Sales A", ScopeMode: auth.ScopeAll})
}
func newTestService(now time.Time) (*Service, *fakeRepo, *fakeAudit) {
	repo := &fakeRepo{}
	writer := &fakeAudit{}
	random := &deterministicRandom{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", PhoneMasked: "138****8000"}
	return NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "platform-subject-123456789", AccountNo: "PA-1"}}, &fakePortal{mapping: PortalMapping{PortalAccountID: "42"}}, writer, []byte(strings.Repeat("p", 32)), "https://portal.example/customer-portal", fixedClock{now}, random, testOperationProtector{}), repo, writer
}

func TestCreateReturnsOneTimeTokenButNeverLeaksRawIdentityOrTokenToPersistence(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, repo, writer := newTestService(now)
	result, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.ActivationURL, "https://portal.example/customer-portal/activate?token=") {
		t.Fatalf("activation URL=%q", result.ActivationURL)
	}
	if result.ExpiresAt.Sub(now) != 2*time.Hour {
		t.Fatalf("expiry=%s", result.ExpiresAt)
	}
	if result.IdentitySummary == "platform-subject-123456789" || !strings.Contains(result.IdentitySummary, "…") {
		t.Fatalf("identity not masked: %q", result.IdentitySummary)
	}
	if result.LoginAccount != "PA-1" {
		t.Fatalf("login account=%q, want PA-1", result.LoginAccount)
	}
	raw := strings.TrimPrefix(result.ActivationURL, "https://portal.example/customer-portal/activate?token=")
	persisted, _ := json.Marshal(struct {
		Invites []*Invite
		Links   []*IdentityLink
		Events  []audit.Event
	}{repo.invites, repo.links, writer.events})
	if strings.Contains(string(persisted), raw) {
		t.Fatal("raw invitation token leaked to persistence or audit")
	}
	if len(repo.invites) != 1 || len(repo.invites[0].TokenHash) != 64 {
		t.Fatalf("unexpected persisted invite: %#v", repo.invites)
	}
}

func TestVerifyDoesNotConsumeAndConsumeIsSubjectBoundAndSingleUse(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, repo, _ := newTestService(now)
	created, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-2"})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(created.ActivationURL, "https://portal.example/customer-portal/activate?token=")
	verified, err := service.Verify(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PlatformUserID != "platform-subject-123456789" || repo.invites[0].Status != StatusPending || repo.consumeCalls != 0 {
		t.Fatalf("verify consumed or wrong DTO: %#v", verified)
	}
	if err = service.Consume(context.Background(), ConsumeRequest{Token: token, PlatformUserID: "attacker-subject", RequestID: "callback-1"}); !errors.Is(err, ErrSubjectMismatch) {
		t.Fatalf("mismatch error=%v", err)
	}
	if repo.invites[0].Status != StatusPending {
		t.Fatal("subject mismatch consumed token")
	}
	if err = service.Consume(context.Background(), ConsumeRequest{Token: token, PlatformUserID: "platform-subject-123456789", RequestID: "callback-2"}); err != nil {
		t.Fatal(err)
	}
	if repo.links[0].Status != "ACTIVE" || repo.links[0].LastVerifiedAt == nil {
		t.Fatalf("identity link did not activate: %#v", repo.links[0])
	}
	if err = service.Consume(context.Background(), ConsumeRequest{Token: token, PlatformUserID: "platform-subject-123456789", RequestID: "callback-3"}); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
}

func TestConsumeRejectsMismatchedIdentityLinkAndRollsBackAuditFailure(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, repo, writer := newTestService(now)
	created, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-consume-rollback"})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(created.ActivationURL, "https://portal.example/customer-portal/activate?token=")
	repo.links[0].PortalAccountID = "replacement-account"
	if err = service.Consume(context.Background(), ConsumeRequest{Token: token, PlatformUserID: "platform-subject-123456789", RequestID: "callback-wrong-link"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("wrong link error=%v", err)
	}
	if repo.invites[0].Status != StatusPending || repo.links[0].Status != StatusPending {
		t.Fatal("wrong identity binding changed state")
	}
	repo.links[0].PortalAccountID = repo.invites[0].PortalAccountID
	writer.err = errors.New("audit unavailable")
	if err = service.Consume(context.Background(), ConsumeRequest{Token: token, PlatformUserID: "platform-subject-123456789", RequestID: "callback-audit-failure"}); err == nil {
		t.Fatal("expected audit failure")
	}
	if repo.invites[0].Status != StatusPending || repo.links[0].Status != StatusPending || repo.links[0].LastVerifiedAt != nil {
		t.Fatalf("audit failure was not atomic: invite=%s link=%s", repo.invites[0].Status, repo.links[0].Status)
	}
}

func TestExpiredInviteCannotBeVerifiedOrConsumed(t *testing.T) {
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	service, repo, _ := newTestService(now)
	token := "expired-token"
	repo.invites = []*Invite{{Model: database.Model{ID: 1, TenantID: "tenant-a", Version: 1}, TokenHash: service.tokenHash(token), Status: StatusPending, ExpiresAt: now.Add(-time.Second)}}
	if _, err := service.Verify(context.Background(), token); !errors.Is(err, ErrExpired) {
		t.Fatalf("verify error=%v", err)
	}
	if repo.invites[0].Status != StatusExpired {
		t.Fatalf("status=%s", repo.invites[0].Status)
	}
	if err := service.Consume(context.Background(), ConsumeRequest{Token: token, PlatformUserID: "subject", RequestID: "callback"}); !errors.Is(err, ErrExpired) {
		t.Fatalf("consume error=%v", err)
	}
}

func TestPartialExternalCompletionCreatesObservableCompensation(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeRepo{}
	random := &deterministicRandom{}
	identity := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000"}
	service := NewService(repo, fakeCustomer{identity: identity}, &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "subject", AccountNo: "account"}}, &fakePortal{err: errors.New("Portal down")}, &fakeAudit{}, []byte(strings.Repeat("p", 32)), "https://portal.example", fixedClock{now}, random, testOperationProtector{})
	if _, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "create-partial"}); err == nil {
		t.Fatal("expected dependency failure")
	}
	if len(repo.compensations) != 1 || repo.compensations[0].TaskType != CompensationMapping || repo.compensations[0].LastErrorCode != "PORTAL_MAPPING_FAILED" {
		t.Fatalf("compensation=%#v", repo.compensations)
	}
	if repo.compensations[0].PlatformUserID != "subject" || repo.compensations[0].AccountNo != "account" {
		t.Fatalf("compensation replay snapshot=%#v", repo.compensations[0])
	}
}

func TestCreateRequiresActorBoundIdempotencyAndExactReplayDoesNotRepeatRemoteWrites(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	platform := &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "subject-a", AccountNo: "account-a"}}
	portal := &fakePortal{mapping: PortalMapping{PortalAccountID: "PA9"}}
	contact := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000"}
	service := NewService(repo, fakeCustomer{identity: contact}, platform, portal, &fakeAudit{}, []byte(strings.Repeat("p", 32)), "https://portal.example", fixedClock{now}, &deterministicRandom{}, testOperationProtector{})

	if result, err := service.Create(serviceContext(), 7, CreateRequest{}); result != nil || !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("missing key result=%#v err=%v", result, err)
	}
	first, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "stable-key"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "stable-key"})
	if err != nil {
		t.Fatal(err)
	}
	if first.InviteNo != second.InviteNo || first.ActivationURL != second.ActivationURL || platform.provisionCalls != 1 || platform.roleCalls != 1 || portal.calls != 1 {
		t.Fatalf("first=%#v second=%#v calls=%d/%d/%d", first, second, platform.provisionCalls, platform.roleCalls, portal.calls)
	}
	if len(repo.operations) != 1 || strings.Contains(string(repo.operations[0].ContactSnapshotCipher), contact.Phone) || strings.Contains(string(repo.operations[0].TokenCipher), strings.TrimPrefix(first.ActivationURL, "https://portal.example/activate?token=")) {
		t.Fatalf("operation leaked plaintext or duplicated: %#v", repo.operations)
	}
	if _, err = service.Create(serviceContext(), 8, CreateRequest{IdempotencyKey: "stable-key"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("payload conflict error=%v", err)
	}
}

func TestCreateResumesFailedMappingWithSameRemoteIdempotencyKey(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	repo := &fakeRepo{}
	platform := &fakePlatform{identity: ProvisionedIdentity{PlatformUserID: "subject-a", AccountNo: "account-a"}}
	portal := &fakePortal{err: errors.New("ambiguous transport failure")}
	contact := ContactIdentity{TenantID: "tenant-a", CustomerID: 7, ContactID: 9, DisplayName: "登记人", Phone: "13800138000", Email: "contact@example.test"}
	service := NewService(repo, fakeCustomer{identity: contact}, platform, portal, &fakeAudit{}, []byte(strings.Repeat("p", 32)), "https://portal.example", fixedClock{now}, &deterministicRandom{}, testOperationProtector{})

	if _, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "retry-key"}); err == nil {
		t.Fatal("expected first ambiguous mapping failure")
	}
	portal.err, portal.mapping = nil, PortalMapping{PortalAccountID: "PA9"}
	result, err := service.Create(serviceContext(), 7, CreateRequest{IdempotencyKey: "retry-key"})
	if err != nil {
		t.Fatal(err)
	}
	if result.InviteNo == "" || platform.provisionCalls != 1 || platform.roleCalls != 1 || portal.calls != 2 || len(portal.keys) != 2 || portal.keys[0] != portal.keys[1] {
		t.Fatalf("result=%#v calls=%d/%d/%d keys=%v", result, platform.provisionCalls, platform.roleCalls, portal.calls, portal.keys)
	}
	if len(repo.compensations) != 1 || repo.compensations[0].TaskNo != portal.keys[0] {
		t.Fatalf("compensation did not share remote key: %#v", repo.compensations)
	}
}
