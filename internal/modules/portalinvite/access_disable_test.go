package portalinvite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
)

type disableRepoFake struct {
	link       *IdentityLink
	operations []*AccessDisableOperation
	revoked    int
}

func (r *disableRepoFake) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *disableRepoFake) LockCustomer(context.Context, string, uint64) error { return nil }
func (r *disableRepoFake) RevokePending(context.Context, string, uint64, string, string, time.Time) error {
	r.revoked++
	return nil
}
func (r *disableRepoFake) FindIdentityLink(_ context.Context, tenant string, customer uint64) (*IdentityLink, error) {
	if r.link == nil || r.link.TenantID != tenant || r.link.CustomerID != customer || r.link.Status == "DISABLED" {
		return nil, ErrNotFound
	}
	return r.link, nil
}
func (r *disableRepoFake) FindIdentityLinkForUpdate(ctx context.Context, tenant string, customer uint64) (*IdentityLink, error) {
	return r.FindIdentityLink(ctx, tenant, customer)
}
func (r *disableRepoFake) FindLatestAccessDisableOperation(_ context.Context, tenant string, customer uint64) (*AccessDisableOperation, error) {
	for index := len(r.operations) - 1; index >= 0; index-- {
		if r.operations[index].TenantID == tenant && r.operations[index].CustomerID == customer {
			return r.operations[index], nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *disableRepoFake) FindAccessDisableOperation(_ context.Context, tenant, actor, key string) (*AccessDisableOperation, error) {
	for _, operation := range r.operations {
		if operation.TenantID == tenant && operation.ActorID == actor && operation.IdempotencyKey == key {
			return operation, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *disableRepoFake) FindAccessDisableOperationForUpdate(_ context.Context, tenant string, id uint64) (*AccessDisableOperation, error) {
	for _, operation := range r.operations {
		if operation.TenantID == tenant && operation.ID == id {
			return operation, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *disableRepoFake) ClaimAccessDisableOperation(_ context.Context, operation *AccessDisableOperation, owner string, now, until time.Time) error {
	if operation.LockedUntil != nil && operation.LockedUntil.After(now) && operation.LockedBy != owner {
		return ErrVersionConflict
	}
	operation.Status, operation.LockedBy, operation.LockedUntil = DisableStatusProcessing, owner, &until
	operation.Version++
	return nil
}
func (r *disableRepoFake) CreateAccessDisableOperation(_ context.Context, operation *AccessDisableOperation) error {
	operation.ID = uint64(len(r.operations) + 1)
	r.operations = append(r.operations, operation)
	return nil
}
func (r *disableRepoFake) AdvanceAccessDisableOperation(_ context.Context, operation *AccessDisableOperation, expected string, updates map[string]any, now time.Time) error {
	if operation.Stage != expected {
		return ErrVersionConflict
	}
	operation.Version++
	operation.UpdatedAt = now
	if value, ok := updates["stage"].(string); ok {
		operation.Stage = value
	}
	if value, ok := updates["status"].(string); ok {
		operation.Status = value
	}
	if value, ok := updates["last_error_code"].(string); ok {
		operation.LastErrorCode = value
	}
	if value, ok := updates["last_error_summary"].(string); ok {
		operation.LastErrorSummary = value
	}
	if value, ok := updates["completed_at"].(time.Time); ok {
		operation.CompletedAt = &value
	}
	if _, ok := updates["attempts"]; ok {
		operation.Attempts++
	}
	if value, ok := updates["next_retry_at"].(time.Time); ok {
		operation.NextRetryAt = &value
	} else if _, ok := updates["next_retry_at"]; ok {
		operation.NextRetryAt = nil
	}
	if value, ok := updates["locked_by"].(string); ok {
		operation.LockedBy = value
	}
	if value, ok := updates["locked_until"].(time.Time); ok {
		operation.LockedUntil = &value
	} else if _, ok := updates["locked_until"]; ok {
		operation.LockedUntil = nil
	}
	return nil
}
func (r *disableRepoFake) DisableIdentityLink(_ context.Context, operation *AccessDisableOperation, _ string, _ time.Time) error {
	if r.link == nil || r.link.ID != operation.IdentityLinkID || r.link.Version != operation.IdentityLinkVersion ||
		r.link.ContactID != operation.ContactID || r.link.PlatformUserID != operation.PlatformUserID || r.link.PortalAccountID != operation.PortalAccountID {
		return ErrVersionConflict
	}
	r.link.Status = "DISABLED"
	r.link.Version++
	return nil
}
func (r *disableRepoFake) HasBlockingAccessDisable(_ context.Context, tenant string, customer uint64) (bool, error) {
	for _, operation := range r.operations {
		if operation.TenantID == tenant && operation.CustomerID == customer &&
			(operation.Status == DisableStatusProcessing || operation.Status == DisableStatusRetryWait || operation.Status == DisableStatusDeadLetter) {
			return true, nil
		}
	}
	return false, nil
}

type disableCustomerFake struct {
	allowed bool
	calls   int
}

func (f *disableCustomerFake) CanAccessCustomer(context.Context, auth.Principal, uint64) (bool, error) {
	f.calls++
	return f.allowed, nil
}

type mappingDisablerFake struct {
	keys []string
	err  error
}

func (f *mappingDisablerFake) DisableMapping(_ context.Context, _ string, _ uint64, _ string, _ string, key string) error {
	f.keys = append(f.keys, key)
	return f.err
}

type roleRevokerFake struct {
	keys []string
	err  error
}

func (f *roleRevokerFake) RevokePortalRole(_ context.Context, _ string, key string) error {
	f.keys = append(f.keys, key)
	return f.err
}

type disableAuditFake struct {
	events []audit.Event
	err    error
}

func (f *disableAuditFake) Write(_ context.Context, event audit.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

type mutableClock struct{ now time.Time }

func (c *mutableClock) Now() time.Time { return c.now }

func disableContext(permission bool) context.Context {
	permissions := map[string]struct{}{}
	if permission {
		permissions["portal_account.disable"] = struct{}{}
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "admin-a", Permissions: permissions, ScopeMode: auth.ScopeAll})
	return requestctx.WithID(ctx, "request-a")
}

func newDisableFixture(now time.Time) (*AccessDisableService, *disableRepoFake, *mappingDisablerFake, *roleRevokerFake, *disableAuditFake, *disableCustomerFake) {
	repo := &disableRepoFake{link: &IdentityLink{
		Model: database.Model{ID: 11, TenantID: "tenant-a", Version: 3}, CustomerID: 7, ContactID: 9,
		PlatformUserID: "subject-a", PortalAccountID: "portal-a", Status: "ACTIVE",
	}}
	customers := &disableCustomerFake{allowed: true}
	portal, platform, writer := &mappingDisablerFake{}, &roleRevokerFake{}, &disableAuditFake{}
	service := NewAccessDisableService(repo, customers, platform, portal, writer, &mutableClock{now: now}, &deterministicRandom{})
	return service, repo, portal, platform, writer, customers
}

func TestAccessDisableConvergesExactMappingSessionsRoleAndAudit(t *testing.T) {
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	service, repo, portal, platform, writer, _ := newDisableFixture(now)
	result, err := service.Disable(disableContext(true), 7, DisableAccessRequest{Reason: "customer terminated access", IdempotencyKey: "disable-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "DISABLED" || result.CustomerID != 7 || result.OperationNo == "" || result.CompletedAt == nil {
		t.Fatalf("result=%#v", result)
	}
	if repo.link.Status != "DISABLED" || repo.revoked != 1 || len(repo.operations) != 1 || repo.operations[0].Status != DisableStatusCompleted {
		t.Fatalf("repo=%#v operation=%#v", repo, repo.operations)
	}
	if len(portal.keys) != 1 || len(platform.keys) != 1 || portal.keys[0] != result.OperationNo+"M" || platform.keys[0] != result.OperationNo+"R" {
		t.Fatalf("unstable remote keys: portal=%v platform=%v", portal.keys, platform.keys)
	}
	if len(writer.events) != 1 || writer.events[0].Operation != "DISABLE_ACCESS" || strings.Contains(string(writer.events[0].AfterJSON), "subject-a") {
		t.Fatalf("unsafe/missing audit: %#v", writer.events)
	}
}

func TestAccessDisableRemoteFailurePersistsSafeRetryWithoutCallingNextStep(t *testing.T) {
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	service, repo, portal, platform, _, _ := newDisableFixture(now)
	portal.err = errors.New("client_secret=never-persist customer@example.test")
	if _, err := service.Disable(disableContext(true), 7, DisableAccessRequest{Reason: "security response", IdempotencyKey: "disable-2"}); err == nil {
		t.Fatal("expected dependency failure")
	}
	operation := repo.operations[0]
	if operation.Status != DisableStatusRetryWait || operation.Attempts != 1 || operation.NextRetryAt == nil || operation.LockedBy != "" || operation.LockedUntil != nil {
		t.Fatalf("retry operation=%#v", operation)
	}
	if strings.Contains(operation.LastErrorSummary, "never-persist") || operation.LastErrorCode != "PORTAL_MAPPING_DISABLE_FAILED" || len(platform.keys) != 0 || repo.link.Status != "ACTIVE" {
		t.Fatalf("unsafe/incorrect failure state: operation=%#v platform=%v link=%s", operation, platform.keys, repo.link.Status)
	}
}

func TestAccessDisablePermissionFailsBeforeCustomerOrRemoteReads(t *testing.T) {
	service, repo, portal, platform, _, customers := newDisableFixture(time.Now().UTC())
	if _, err := service.Disable(disableContext(false), 7, DisableAccessRequest{Reason: "not authorized", IdempotencyKey: "disable-3"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
	if customers.calls != 0 || len(repo.operations) != 0 || len(portal.keys) != 0 || len(platform.keys) != 0 {
		t.Fatalf("unauthorized request touched dependencies: customers=%d repo=%d portal=%d platform=%d", customers.calls, len(repo.operations), len(portal.keys), len(platform.keys))
	}
}

func TestAccessDisableRejectsSecondBusinessCommandBeforeRemoteDispatch(t *testing.T) {
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	service, repo, portal, platform, _, _ := newDisableFixture(now)
	repo.operations = []*AccessDisableOperation{{
		Model: database.Model{ID: 20, TenantID: "tenant-a", Version: 1}, CustomerID: 7, IdentityLinkID: repo.link.ID,
		OperationNo: "PDEXISTING", ActorID: "another-admin", IdempotencyKey: "existing-key", Stage: DisableStagePrepared, Status: DisableStatusRetryWait,
	}}
	if _, err := service.Disable(disableContext(true), 7, DisableAccessRequest{Reason: "second command", IdempotencyKey: "disable-new"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("error=%v", err)
	}
	if len(repo.operations) != 1 || len(portal.keys) != 0 || len(platform.keys) != 0 {
		t.Fatalf("duplicate command reached persistence or remote calls: operations=%d portal=%v platform=%v", len(repo.operations), portal.keys, platform.keys)
	}
}

func TestAccessStatusDoesNotLetHistoricalDisableShadowNewExplicitGrant(t *testing.T) {
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	service, repo, _, _, _, _ := newDisableFixture(now)
	repo.operations = []*AccessDisableOperation{{
		Model: database.Model{ID: 20, TenantID: "tenant-a", Version: 4}, CustomerID: 7, IdentityLinkID: 10,
		OperationNo: "PDOLD", Stage: DisableStageCompleted, Status: DisableStatusCompleted, CompletedAt: &now,
	}}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "admin-a", Permissions: map[string]struct{}{"portal_account.disable": {}}, ScopeMode: auth.ScopeAll})
	result, err := service.Current(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessStatus != "ACTIVE" || result.OperationNo != "" {
		t.Fatalf("historical disable shadowed new grant: %#v", result)
	}
}
