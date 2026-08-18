package opportunity

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type createIdempotencyRepository struct {
	*GORMRepository
	visible          bool
	visibilityChecks int
	findCalls        int
	findSequence     []*CreateIdempotency
	replay           *CreateIdempotency
	resource         *Opportunity
	created          int
	numbered         int
	idemCreates      int
	idemCreateErr    error
}

func (r *createIdempotencyRepository) CustomerVisible(_ context.Context, _ auth.Principal, _ uint64) (bool, error) {
	r.visibilityChecks++
	return r.visible, nil
}

func (r *createIdempotencyRepository) NextNumber(_ context.Context, _, _ string) (string, error) {
	r.numbered++
	return "SJ202608010001", nil
}

func (r *createIdempotencyRepository) Create(_ context.Context, model *Opportunity) error {
	r.created++
	model.ID = 17
	model.CreatedAt = time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	model.UpdatedAt = model.CreatedAt
	r.resource = model
	return nil
}

func (r *createIdempotencyRepository) FindCreateIdempotency(_ context.Context, _, _, _ string) (*CreateIdempotency, error) {
	r.findCalls++
	if len(r.findSequence) > 0 {
		value := r.findSequence[0]
		r.findSequence = r.findSequence[1:]
		return value, nil
	}
	return r.replay, nil
}

func (r *createIdempotencyRepository) CreateCreateIdempotency(_ context.Context, model *CreateIdempotency) error {
	r.idemCreates++
	if r.idemCreateErr == nil {
		r.replay = model
	}
	return r.idemCreateErr
}

func (r *createIdempotencyRepository) FindCreatedByID(_ context.Context, _ string, _ uint64) (*Opportunity, error) {
	if r.resource == nil {
		return nil, ErrNotFound
	}
	return r.resource, nil
}

type countingAuditWriter struct{ writes int }

func (w *countingAuditWriter) Write(context.Context, audit.Event) error {
	w.writes++
	return nil
}

func createTestInput() CreateRequest {
	return CreateRequest{
		Name: " 商机 ", CustomerID: 3, Type: " 新购 ", Source: " 线索 ",
		ExpectedAmount: " 100.00 ", ExpectedSignDate: "2026-08-31",
		RequirementSummary: " 需求 ", SystemCount: 2, PainPoints: " 痛点 ",
		CompetitorInfo: " 竞品 ", OwnerUserID: " owner-a ", OwnerOrgID: " org-a ",
		IdempotencyKey: " create-key ",
	}
}

func createTestService(repo Repository, writer audit.Writer) *Service {
	return &Service{
		repo: repo, audit: writer,
		now:               func() time.Time { return time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC) },
		createTransaction: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
	}
}

func createTestContext(principal auth.Principal) context.Context {
	if principal.PrimaryOrgID == "" {
		principal.PrimaryOrgID = "org-a"
		principal.OrganizationIDs = []string{"org-a"}
	}
	return auth.WithPrincipal(context.Background(), principal)
}

func TestCreateRequiresBoundedIdempotencyKey(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	service := createTestService(&createIdempotencyRepository{GORMRepository: &GORMRepository{}}, &countingAuditWriter{})
	input := createTestInput()
	input.IdempotencyKey = " "
	if _, err := service.Create(createTestContext(principal), input); !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("missing key error=%v", err)
	}
	input.IdempotencyKey = string(make([]byte, 129))
	if _, err := service.Create(createTestContext(principal), input); !errors.Is(err, ErrIdempotencyKeyTooLong) {
		t.Fatalf("long key error=%v", err)
	}
}

func TestCreateAlwaysInheritsAuthenticatedOwner(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", PrimaryOrgID: "sales-org-a", OrganizationIDs: []string{"sales-org-a"}}
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	service := createTestService(repo, &countingAuditWriter{})
	input := createTestInput()
	input.OwnerUserID, input.OwnerOrgID = "other-user", "other-org"

	result, err := service.Create(createTestContext(principal), input)
	if err != nil || result == nil || result.OwnerUserID != "sales-a" || result.OwnerOrgID != "sales-org-a" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if repo.resource == nil || repo.resource.OwnerUserID != "sales-a" || repo.resource.OwnerOrgID != "sales-org-a" {
		t.Fatalf("persisted owner=%#v", repo.resource)
	}
	if repo.resource.ContractLinkStatus != "PENDING" {
		t.Fatalf("initial contract link status = %q, want PENDING", repo.resource.ContractLinkStatus)
	}
}

func TestCreateExactReplayPreservesOriginalResponseWithoutDuplicateNumberOrAudit(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	writer := &countingAuditWriter{}
	service := createTestService(repo, writer)
	first, err := service.Create(createTestContext(principal), createTestInput())
	if err != nil {
		t.Fatal(err)
	}
	// A later ordinary update must not change the historical creation response.
	repo.resource.Name = "后来修改的名称"
	repo.resource.Version = 9
	second, err := service.Create(createTestContext(principal), createTestInput())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Name != "商机" || second.Version != 1 {
		t.Fatalf("first=%#v replay=%#v", first, second)
	}
	if repo.numbered != 1 || repo.created != 1 || repo.idemCreates != 1 || writer.writes != 1 {
		t.Fatalf("numbered=%d created=%d idem=%d audit=%d", repo.numbered, repo.created, repo.idemCreates, writer.writes)
	}
}

func TestCreateRejectsSameKeyDifferentNormalizedPayload(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	service := createTestService(repo, &countingAuditWriter{})
	if _, err := service.Create(createTestContext(principal), createTestInput()); err != nil {
		t.Fatal(err)
	}
	changed := createTestInput()
	changed.ExpectedAmount = "100.0"
	if _, err := service.Create(createTestContext(principal), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different payload error=%v", err)
	}
}

func TestCreateAuthorizationPrecedesReplayLookup(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: false, replay: &CreateIdempotency{Key: "create-key"}}
	service := createTestService(repo, &countingAuditWriter{})
	if _, err := service.Create(createTestContext(principal), createTestInput()); !errors.Is(err, ErrCustomerForbidden) {
		t.Fatalf("authorization error=%v", err)
	}
	if repo.findCalls != 0 || repo.numbered != 0 {
		t.Fatalf("replay was probed before authorization: find=%d numbered=%d", repo.findCalls, repo.numbered)
	}
}

func TestCreateReplayRejectsResourceCreatedByAnotherActor(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	principal.PrimaryOrgID = "org-a"
	input := inheritCreateOwner(normalizeCreateRequest(createTestInput()), principal)
	hash, err := createRequestHash(input)
	if err != nil {
		t.Fatal(err)
	}
	response := Response{ID: 17, CustomerID: 3, OpportunityNo: "SJ202608010001", Version: 1}
	encoded := audit.JSON(response)
	repo := &createIdempotencyRepository{
		GORMRepository: &GORMRepository{}, visible: true,
		replay:   &CreateIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: "create-key", CustomerID: 3, OpportunityID: 17, RequestHash: hash, ResponseHash: bytesHash(encoded), ResponseJSON: encoded},
		resource: &Opportunity{CustomerID: 3},
	}
	repo.resource.ID = 17
	repo.resource.TenantID = principal.TenantID
	repo.resource.CreatedBy = "actor-b"
	service := createTestService(repo, &countingAuditWriter{})
	if _, err = service.Create(createTestContext(principal), createTestInput()); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-actor resource error=%v", err)
	}
}

func TestCreateDuplicateKeyRaceRecoversOnlyStrictWinner(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	principal.PrimaryOrgID = "org-a"
	input := inheritCreateOwner(normalizeCreateRequest(createTestInput()), principal)
	hash, _ := createRequestHash(input)
	response := Response{ID: 17, CustomerID: 3, OpportunityNo: "SJ202608010001", Version: 1}
	encoded := audit.JSON(response)
	winner := &CreateIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: "create-key", CustomerID: 3, OpportunityID: 17, RequestHash: hash, ResponseHash: bytesHash(encoded), ResponseJSON: encoded}
	resource := &Opportunity{CustomerID: 3}
	resource.ID, resource.TenantID, resource.CreatedBy = 17, principal.TenantID, principal.UserID
	repo := &createIdempotencyRepository{
		GORMRepository: &GORMRepository{}, visible: true, resource: resource,
		findSequence:  [](*CreateIdempotency){nil, winner},
		idemCreateErr: &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry 'tenant-a-actor-a-create-key' for key 'uq_opportunity_create_idem'"},
	}
	service := createTestService(repo, &countingAuditWriter{})
	result, err := service.Create(createTestContext(principal), createTestInput())
	if err != nil || result.ID != 17 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	bad := *winner
	bad.RequestHash = "different"
	repo.findSequence = [](*CreateIdempotency){nil, &bad}
	if _, err = service.Create(createTestContext(principal), createTestInput()); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("unbound winner error=%v", err)
	}
}

func TestCreateDoesNotRecoverUnrelatedBusinessDuplicate(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	duplicate := &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uk_opportunity_no'"}
	repo := &createIdempotencyRepository{
		GORMRepository: &GORMRepository{}, visible: true,
		idemCreateErr: duplicate,
	}
	service := createTestService(repo, &countingAuditWriter{})
	if _, err := service.Create(createTestContext(principal), createTestInput()); !errors.Is(err, duplicate) {
		t.Fatalf("unrelated duplicate was hidden: %v", err)
	}
	if repo.findCalls != 1 {
		t.Fatalf("unexpected recovery replay lookup count=%d", repo.findCalls)
	}
}

func TestCreateReplayRejectsTamperedResponseSnapshot(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	principal.PrimaryOrgID = "org-a"
	input := inheritCreateOwner(normalizeCreateRequest(createTestInput()), principal)
	hash, _ := createRequestHash(input)
	encoded := audit.JSON(Response{ID: 17, CustomerID: 3, OpportunityNo: "SJ202608010001", Version: 1})
	replay := &CreateIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: "create-key", CustomerID: 3, OpportunityID: 17, RequestHash: hash, ResponseHash: bytesHash(encoded), ResponseJSON: encoded}
	replay.ResponseJSON = audit.JSON(Response{ID: 17, CustomerID: 3, OpportunityNo: "TAMPERED", Version: 1})
	resource := &Opportunity{CustomerID: 3}
	resource.ID, resource.TenantID, resource.CreatedBy = 17, principal.TenantID, principal.UserID
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true, replay: replay, resource: resource}
	service := createTestService(repo, &countingAuditWriter{})
	if _, err := service.Create(createTestContext(principal), createTestInput()); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("tampered snapshot error=%v", err)
	}
}

func TestCreateSameKeyIsIsolatedAcrossActors(t *testing.T) {
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	service := createTestService(repo, &countingAuditWriter{})
	actorA := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	if _, err := service.Create(createTestContext(actorA), createTestInput()); err != nil {
		t.Fatal(err)
	}
	// The repository stub emulates the actor-qualified lookup by hiding actor A's
	// replay. Actor B is entitled to independently consume the same opaque key.
	repo.replay = nil
	actorB := auth.Principal{TenantID: "tenant-a", UserID: "actor-b"}
	if _, err := service.Create(createTestContext(actorB), createTestInput()); err != nil {
		t.Fatal(err)
	}
	if repo.created != 2 || repo.numbered != 2 {
		t.Fatalf("cross-actor key was not isolated: created=%d numbered=%d", repo.created, repo.numbered)
	}
}

func TestValidateCreateReplayIsTenantAndActorBound(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	value := &CreateIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: "key", CustomerID: 3, OpportunityID: 17, RequestHash: "hash", ResponseHash: string(make([]byte, 64))}
	if err := validateCreateReplay(value, principal, 3, "key", "hash"); err != nil {
		t.Fatal(err)
	}
	for _, altered := range []auth.Principal{{TenantID: "tenant-b", UserID: "actor-a"}, {TenantID: "tenant-a", UserID: "actor-b"}} {
		if err := validateCreateReplay(value, altered, 3, "key", "hash"); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("principal=%#v error=%v", altered, err)
		}
	}
}

func TestOpportunityCreateIdempotencyMigrationHasSafeCompositeConstraints(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000045_opportunity_create_idempotency.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000045_opportunity_create_idempotency.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"actor_id VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL",
		"idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
		"UNIQUE KEY uq_opportunity_create_idem (tenant_id,actor_id,idempotency_key)",
		"UNIQUE KEY uq_opportunity_create_resource (tenant_id,opportunity_id)",
		"FOREIGN KEY (tenant_id,customer_id)",
		"REFERENCES crm_customers(tenant_id,id)",
		"FOREIGN KEY (tenant_id,opportunity_id)",
		"REFERENCES crm_opportunities(tenant_id,id)",
		"request_hash CHAR(64)", "response_hash CHAR(64)", "response_json JSON",
		"CHECK (CHAR_LENGTH(TRIM(idempotency_key)) BETWEEN 1 AND 128)",
		"request_hash REGEXP '^[0-9a-f]{64}$'", "response_hash REGEXP '^[0-9a-f]{64}$'",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"request_json", "request_body", "INSERT INTO crm_opportunity_create_idempotency"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("migration contains forbidden request/history material %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "forward repair") || !strings.Contains(string(down), "DROP TABLE IF EXISTS crm_opportunity_create_idempotency") {
		t.Fatalf("unsafe rollback guidance: %s", down)
	}
}
