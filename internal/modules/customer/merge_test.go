package customer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type mergeRepoFake struct {
	mu              sync.Mutex
	source          Customer
	target          Customer
	blockers        []MergeBlocker
	counts          MergeMigrationCounts
	migrated        bool
	logs            []MergeLog
	changes         []ChangeLog
	idem            map[string]MergeIdempotency
	outboxes        []MergeOutboxEvent
	failAt          string
	sourceContacts  []bool
	targetContacts  []bool
	relationsLocked bool
}

type mergeSnapshot struct {
	source, target  Customer
	migrated        bool
	sourceContacts  []bool
	targetContacts  []bool
	relationsLocked bool
	logs            []MergeLog
	changes         []ChangeLog
	idem            map[string]MergeIdempotency
	outboxes        []MergeOutboxEvent
}

func (r *mergeRepoFake) snapshot() mergeSnapshot {
	idem := make(map[string]MergeIdempotency, len(r.idem))
	for key, value := range r.idem {
		idem[key] = value
	}
	return mergeSnapshot{source: r.source, target: r.target, migrated: r.migrated, sourceContacts: append([]bool(nil), r.sourceContacts...), targetContacts: append([]bool(nil), r.targetContacts...), relationsLocked: r.relationsLocked, logs: append([]MergeLog(nil), r.logs...), changes: append([]ChangeLog(nil), r.changes...), idem: idem, outboxes: append([]MergeOutboxEvent(nil), r.outboxes...)}
}

func (r *mergeRepoFake) restore(value mergeSnapshot) {
	r.source, r.target, r.migrated = value.source, value.target, value.migrated
	r.sourceContacts, r.targetContacts = value.sourceContacts, value.targetContacts
	r.relationsLocked = value.relationsLocked
	r.logs, r.changes, r.idem, r.outboxes = value.logs, value.changes, value.idem, value.outboxes
}

func (r *mergeRepoFake) WithMergeTransaction(ctx context.Context, fn func(context.Context) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	before := r.snapshot()
	if err := fn(ctx); err != nil {
		r.restore(before)
		return err
	}
	return nil
}

func (r *mergeRepoFake) LockCustomersForMerge(_ context.Context, principal auth.Principal, sourceID, targetID uint64) (*Customer, *Customer, error) {
	if r.source.ID != sourceID || r.target.ID != targetID || r.source.TenantID != principal.TenantID || r.target.TenantID != principal.TenantID || !customerInScope(r.source, principal) || !customerInScope(r.target, principal) {
		return nil, nil, ErrNotFound
	}
	return &r.source, &r.target, nil
}

func customerInScope(value Customer, principal auth.Principal) bool {
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return true
	case auth.ScopeOrg:
		for _, id := range principal.OrganizationIDs {
			if value.OwnerOrgID == id {
				return true
			}
		}
		return false
	default:
		return value.OwnerUserID == principal.UserID
	}
}

func mergeIdemMapKey(tenant, actor, key string) string { return tenant + "\x00" + actor + "\x00" + key }

func (r *mergeRepoFake) FindMergeIdempotency(_ context.Context, tenant, actor, key string) (*MergeIdempotency, error) {
	value, ok := r.idem[mergeIdemMapKey(tenant, actor, key)]
	if !ok {
		return nil, nil
	}
	return &value, nil
}

func (r *mergeRepoFake) LockMergeRelations(context.Context, string, uint64) error {
	r.relationsLocked = true
	return nil
}

func (r *mergeRepoFake) MergeBlockers(context.Context, string, uint64, uint64) ([]MergeBlocker, error) {
	if !r.relationsLocked {
		return nil, errors.New("merge blockers read before source opportunities were locked")
	}
	return append([]MergeBlocker(nil), r.blockers...), nil
}

func (r *mergeRepoFake) MigrateMergeRelations(context.Context, string, uint64, uint64, string, time.Time) (MergeMigrationCounts, error) {
	if !r.relationsLocked {
		return MergeMigrationCounts{}, errors.New("relations migrated without source opportunity lock")
	}
	r.migrated = true
	for range r.sourceContacts {
		r.targetContacts = append(r.targetContacts, false)
	}
	r.sourceContacts = nil
	return r.counts, nil
}

func (r *mergeRepoFake) MarkCustomersMerged(_ context.Context, source, target *Customer, sourceVersion, targetVersion uint64, _ string, now time.Time) error {
	if source.Version != sourceVersion || target.Version != targetVersion {
		return ErrVersionConflict
	}
	target.Version++
	source.Version++
	source.Status, source.MergedIntoID, source.EndDate = StatusMerged, &target.ID, &now
	return nil
}

func (r *mergeRepoFake) CreateMergeLog(_ context.Context, value *MergeLog) error {
	if r.failAt == "log" {
		return errors.New("log failed")
	}
	r.logs = append(r.logs, *value)
	return nil
}

func (r *mergeRepoFake) CreateMergeIdempotency(_ context.Context, value *MergeIdempotency) error {
	mapKey := mergeIdemMapKey(value.TenantID, value.ActorID, value.Key)
	if _, exists := r.idem[mapKey]; exists {
		return errors.New("duplicate idempotency key")
	}
	r.idem[mapKey] = *value
	return nil
}

func (r *mergeRepoFake) CreateMergeOutbox(_ context.Context, value *MergeOutboxEvent) error {
	if r.failAt == "outbox" {
		return errors.New("outbox failed")
	}
	r.outboxes = append(r.outboxes, *value)
	return nil
}

func (r *mergeRepoFake) CreateChangeLog(_ context.Context, value *ChangeLog) error {
	r.changes = append(r.changes, *value)
	return nil
}

type mergeAuditFake struct{ err error }

func (w mergeAuditFake) Write(context.Context, audit.Event) error { return w.err }

func newMergeTestService(repo *mergeRepoFake, writer audit.Writer) *Service {
	if repo.idem == nil {
		repo.idem = map[string]MergeIdempotency{}
	}
	return &Service{merge: repo, audit: writer, now: func() time.Time { return time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC) }}
}

func newMergeRepo() *mergeRepoFake {
	return &mergeRepoFake{
		source: Customer{Model: database.Model{ID: 1, TenantID: "tenant-a", Version: 3}, OwnerUserID: "user-a", OwnerOrgID: "org-a", Status: StatusActive},
		target: Customer{Model: database.Model{ID: 2, TenantID: "tenant-a", Version: 5}, OwnerUserID: "user-a", OwnerOrgID: "org-a", Status: StatusActive},
		counts: MergeMigrationCounts{Contacts: 2, Stakeholders: 2, Systems: 3, Followups: 3, Opportunities: 4, PortalInvites: 1}, idem: map[string]MergeIdempotency{},
		sourceContacts: []bool{true, false}, targetContacts: []bool{true, false},
	}
}

func mergeContext(principal auth.Principal) context.Context {
	return requestctx.WithID(auth.WithPrincipal(context.Background(), principal), "request-merge")
}

func allScopePrincipal(user, tenant string) auth.Principal {
	return auth.Principal{UserID: user, TenantID: tenant, ScopeMode: auth.ScopeAll}
}

func validMergeRequest() MergeRequest {
	return MergeRequest{SourceCustomerID: 1, TargetCustomerID: 2, SourceVersion: 3, TargetVersion: 5, Reason: "duplicate master", IdempotencyKey: "merge-key"}
}

func TestMergeMigratesConfirmedRelationsAndReplays(t *testing.T) {
	repo := newMergeRepo()
	service := newMergeTestService(repo, mergeAuditFake{})
	ctx := mergeContext(allScopePrincipal("user-a", "tenant-a"))
	first, err := service.Merge(ctx, validMergeRequest())
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	second, err := service.Merge(ctx, validMergeRequest())
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if first.SourceStatus != StatusMerged || first.MergedIntoID != 2 || first.MigratedCounts != repo.counts || *repo.source.MergedIntoID != 2 {
		t.Fatalf("unexpected result: %#v source=%#v", first, repo.source)
	}
	if first.MigratedCounts.Stakeholders != 2 || first.MigratedCounts.Systems != 3 {
		t.Fatalf("profile children missing from merge result: %#v", first.MigratedCounts)
	}
	if encodedFirst, _ := json.Marshal(first); string(encodedFirst) != string(repo.idem[mergeIdemMapKey("tenant-a", "user-a", "merge-key")].ResponseJSON) {
		t.Fatalf("durable replay response differs: %s", encodedFirst)
	}
	if *second != *first || len(repo.logs) != 1 || len(repo.changes) != 3 || len(repo.outboxes) != 1 {
		t.Fatalf("replay produced side effects: second=%#v logs=%d changes=%d outbox=%d", second, len(repo.logs), len(repo.changes), len(repo.outboxes))
	}
}

func TestMergeRejectsTenantAndScopeBypass(t *testing.T) {
	tests := []struct {
		name      string
		principal auth.Principal
	}{
		{"tenant", allScopePrincipal("user-a", "tenant-b")},
		{"self scope", auth.Principal{UserID: "other", TenantID: "tenant-a", ScopeMode: auth.ScopeSelf}},
		{"org scope", auth.Principal{UserID: "director", TenantID: "tenant-a", ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-b"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newMergeRepo()
			_, err := newMergeTestService(repo, mergeAuditFake{}).Merge(mergeContext(test.principal), validMergeRequest())
			if !errors.Is(err, ErrNotFound) || repo.migrated {
				t.Fatalf("err=%v migrated=%v", err, repo.migrated)
			}
		})
	}
}

func TestMergeRejectsSelfInactiveVersionAndUnsafeRelation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*mergeRepoFake, *MergeRequest)
		want   error
	}{
		{"self", func(_ *mergeRepoFake, input *MergeRequest) { input.TargetCustomerID = input.SourceCustomerID }, ErrMergeSameCustomer},
		{"inactive", func(repo *mergeRepoFake, _ *MergeRequest) { repo.source.Status = StatusVoid }, ErrMergeInactive},
		{"version", func(_ *mergeRepoFake, input *MergeRequest) { input.SourceVersion++ }, ErrVersionConflict},
		{"portal blocker", func(repo *mergeRepoFake, _ *MergeRequest) {
			repo.blockers = []MergeBlocker{{Code: "PORTAL_IDENTITY_LINK", Relation: "portal_identity_links", Count: 1}}
		}, ErrMergeBlocked},
		{"contract blocker", func(repo *mergeRepoFake, _ *MergeRequest) {
			repo.blockers = []MergeBlocker{{Code: "SIGNED_CONTRACT_REBIND_REQUIRED", Relation: "opportunities.contract_ref", Count: 2}}
		}, ErrMergeBlocked},
		{"pending invite conflict", func(repo *mergeRepoFake, _ *MergeRequest) {
			repo.blockers = []MergeBlocker{{Code: "PENDING_INVITE_CONFLICT", Relation: "portal_invites", Count: 2}}
		}, ErrMergeBlocked},
		{"invalid target registration contact", func(repo *mergeRepoFake, _ *MergeRequest) {
			repo.blockers = []MergeBlocker{{Code: "TARGET_REGISTRATION_CONTACT_INVALID", Relation: "customer_contacts", Count: 0}}
		}, ErrMergeBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, input := newMergeRepo(), validMergeRequest()
			test.mutate(repo, &input)
			_, err := newMergeTestService(repo, mergeAuditFake{}).Merge(mergeContext(allScopePrincipal("user-a", "tenant-a")), input)
			matches := errors.Is(err, test.want)
			if test.want == ErrMergeBlocked {
				matches = err != nil && apperror.As(err).Code == ErrMergeBlocked.Code
			}
			if !matches || repo.migrated {
				t.Fatalf("err=%v migrated=%v", err, repo.migrated)
			}
		})
	}
}

func TestMergeKeepsExactlyOneRegistrationContactOnTarget(t *testing.T) {
	repo := newMergeRepo()
	_, err := newMergeTestService(repo, mergeAuditFake{}).Merge(mergeContext(allScopePrincipal("user-a", "tenant-a")), validMergeRequest())
	if err != nil {
		t.Fatal(err)
	}
	registrationCount := 0
	for _, registration := range repo.targetContacts {
		if registration {
			registrationCount++
		}
	}
	if registrationCount != 1 || len(repo.sourceContacts) != 0 || len(repo.targetContacts) != 4 {
		t.Fatalf("source=%v target=%v registration_count=%d", repo.sourceContacts, repo.targetContacts, registrationCount)
	}
}

func TestMergeIdempotencyIsActorAndPayloadBound(t *testing.T) {
	repo := newMergeRepo()
	service := newMergeTestService(repo, mergeAuditFake{})
	input := validMergeRequest()
	ctx := mergeContext(allScopePrincipal("user-a", "tenant-a"))
	if _, err := service.Merge(ctx, input); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Reason = "different payload"
	if _, err := service.Merge(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("payload conflict err=%v", err)
	}
	// A different actor cannot read user-a's replay result. It reaches normal
	// state/scope validation and cannot turn the key into an authorization bypass.
	if _, err := service.Merge(mergeContext(allScopePrincipal("user-b", "tenant-a")), input); !errors.Is(err, ErrMergeInactive) {
		t.Fatalf("actor isolation err=%v", err)
	}
}

func TestMergeReplayDoesNotBypassActorDataScope(t *testing.T) {
	repo := newMergeRepo()
	input := validMergeRequest()
	response, _ := json.Marshal(MergeResponse{SourceCustomerID: 1, TargetCustomerID: 2, SourceStatus: StatusMerged, MergedIntoID: 2})
	repo.idem[mergeIdemMapKey("tenant-a", "user-a", input.IdempotencyKey)] = MergeIdempotency{TenantID: "tenant-a", ActorID: "user-a", Key: input.IdempotencyKey, RequestHash: mergeRequestHash(input), ResponseJSON: response}
	principal := auth.Principal{UserID: "user-a", TenantID: "tenant-a", ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-b"}}
	_, err := newMergeTestService(repo, mergeAuditFake{}).Merge(mergeContext(principal), input)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope replay err=%v", err)
	}
}

func TestMergeRollsBackRelationsWhenLateStepFails(t *testing.T) {
	for _, fail := range []string{"outbox", "audit"} {
		t.Run(fail, func(t *testing.T) {
			repo := newMergeRepo()
			writer := mergeAuditFake{}
			if fail == "outbox" {
				repo.failAt = fail
			} else {
				writer.err = errors.New("audit failed")
			}
			_, err := newMergeTestService(repo, writer).Merge(mergeContext(allScopePrincipal("user-a", "tenant-a")), validMergeRequest())
			if err == nil {
				t.Fatal("expected failure")
			}
			if repo.migrated || repo.source.Status != StatusActive || repo.source.MergedIntoID != nil || repo.target.Version != 5 || len(repo.logs) != 0 || len(repo.changes) != 0 || len(repo.idem) != 0 || len(repo.outboxes) != 0 {
				t.Fatalf("transaction was not rolled back: %#v", repo)
			}
		})
	}
}

func TestMergeConcurrentSameKeyCommitsOnce(t *testing.T) {
	repo := newMergeRepo()
	service := newMergeTestService(repo, mergeAuditFake{})
	ctx := mergeContext(allScopePrincipal("user-a", "tenant-a"))
	const callers = 12
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.Merge(ctx, validMergeRequest())
			if err == nil && (result == nil || result.MergedIntoID != 2) {
				err = errors.New("invalid merge response")
			}
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent replay failed: %v", err)
		}
	}
	if len(repo.logs) != 1 || len(repo.outboxes) != 1 || len(repo.idem) != 1 {
		t.Fatalf("committed more than once: logs=%d outboxes=%d idem=%d", len(repo.logs), len(repo.outboxes), len(repo.idem))
	}
}

func TestLockSourceOpportunitiesSQLUsesTenantCustomerOrderAndUpdateLock(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint64
	statement := db.Raw(lockSourceOpportunitiesSQL, "tenant-a", 7).Scan(&ids).Statement.SQL.String()
	for _, fragment := range []string{"tenant_id", "customer_id", "deleted_at IS NULL", "ORDER BY id", "FOR UPDATE"} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("missing %q in %q", fragment, statement)
		}
	}
}
