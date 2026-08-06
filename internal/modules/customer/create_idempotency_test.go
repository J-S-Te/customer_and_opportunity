package customer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

type createIdempotencyRepoStub struct {
	Repository
	prior             *CreateIdempotency
	resource          *Customer
	duplicates        []DuplicateCandidate
	transactionErr    error
	commitWinner      *CreateIdempotency
	commitResource    *Customer
	findReplayCalls   int
	nextNumberCalls   int
	createCalls       int
	createReplayCalls int
}

func (r *createIdempotencyRepoStub) WithCreateTransaction(ctx context.Context, fn func(context.Context) error) error {
	if r.transactionErr != nil {
		if r.commitWinner != nil {
			copyValue := *r.commitWinner
			r.prior = &copyValue
		}
		if r.commitResource != nil {
			copyValue := *r.commitResource
			r.resource = &copyValue
		}
		return r.transactionErr
	}
	return fn(ctx)
}

func (r *createIdempotencyRepoStub) FindCreateIdempotency(_ context.Context, tenantID, actorID, key string) (*CreateIdempotency, error) {
	r.findReplayCalls++
	if r.prior == nil || r.prior.TenantID != tenantID || r.prior.ActorID != actorID || r.prior.Key != key {
		return nil, nil
	}
	copyValue := *r.prior
	return &copyValue, nil
}

func (r *createIdempotencyRepoStub) FindCreatedCustomer(_ context.Context, tenantID, actorID string, id uint64) (*Customer, error) {
	if r.resource == nil || r.resource.TenantID != tenantID || r.resource.CreatedBy != actorID || r.resource.ID != id {
		return nil, ErrNotFound
	}
	copyValue := *r.resource
	return &copyValue, nil
}

func (r *createIdempotencyRepoStub) CreateCreateIdempotency(_ context.Context, value *CreateIdempotency) error {
	r.createReplayCalls++
	copyValue := *value
	r.prior = &copyValue
	return nil
}

func (r *createIdempotencyRepoStub) FindDuplicates(context.Context, string, string, string, uint64) ([]DuplicateCandidate, error) {
	return append([]DuplicateCandidate(nil), r.duplicates...), nil
}

func (r *createIdempotencyRepoStub) NextNumber(context.Context, string, string) (string, error) {
	r.nextNumberCalls++
	return "KH202608010001", nil
}

func (r *createIdempotencyRepoStub) Create(_ context.Context, value *Customer) error {
	r.createCalls++
	value.ID = 71
	value.CreatedAt = time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	value.UpdatedAt = value.CreatedAt
	for index := range value.Contacts {
		value.Contacts[index].ID = uint64(100 + index)
		value.Contacts[index].CustomerID = value.ID
	}
	copyValue := *value
	r.resource = &copyValue
	return nil
}

type createAuditStub struct{ writes int }

func (w *createAuditStub) Write(context.Context, audit.Event) error {
	w.writes++
	return nil
}

func TestCustomerCreateHandlerReadsIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(`{"name":"示例客户","unified_credit_code":"91310000TEST","customer_type":"企业","industry":"软件","region":"华东","contacts":[{"name":"张三","phone":"13800138000","email":"zhang@example.com","is_registration":true}],"reason":"新建"}`))
	ginContext.Request.Header.Set("Idempotency-Key", "handler-key")
	ginContext.Request = ginContext.Request.WithContext(auth.WithPrincipal(ginContext.Request.Context(), createTestPrincipal("tenant-a", "user-a")))
	NewHandler(service).Create(ginContext)
	if recorder.Code != http.StatusCreated || repo.prior == nil || repo.prior.Key != "handler-key" || repo.resource.OwnerUserID != "user-a" || repo.resource.OwnerOrgID != "org-a" {
		t.Fatalf("status=%d body=%s replay=%#v", recorder.Code, recorder.Body.String(), repo.prior)
	}
}

func createTestPrincipal(tenantID, userID string) auth.Principal {
	return auth.Principal{TenantID: tenantID, UserID: userID, PrimaryOrgID: "org-a", OrganizationIDs: []string{"org-a"}, Permissions: map[string]struct{}{"customer.create": {}}}
}

func createTestRequest(key string) CreateRequest {
	return CreateRequest{
		Name: " 示例客户 ", UnifiedCreditCode: " 91310000TEST ", CustomerType: " 企业 ",
		Industry: " 软件 ", Region: " 华东 ", OwnerUserID: " owner-a ", OwnerOrgID: " org-a ",
		Contacts: []ContactInput{{Name: " 张三 ", Phone: " 13800138000 ", Email: " zhang@example.com ", IsRegistration: true}},
		Reason:   " 新建 ", IdempotencyKey: key,
	}
}

func newCreateTestService(t *testing.T, repo Repository, writer audit.Writer) *Service {
	t.Helper()
	service := NewService(nil, repo, writer, profileCodec(t))
	service.now = func() time.Time { return time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC) }
	return service
}

func TestCustomerCreateRequiresActorBoundIdempotencyKey(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	ctx := auth.WithPrincipal(context.Background(), createTestPrincipal("tenant-a", "user-a"))
	for _, key := range []string{"", "  ", string(make([]byte, 129))} {
		input := createTestRequest(key)
		result, err := service.Create(ctx, input)
		if result != nil || (!errors.Is(err, ErrIdempotencyRequired) && !errors.Is(err, ErrIdempotencyInvalid)) {
			t.Fatalf("key length=%d result=%#v err=%v", len(key), result, err)
		}
	}
	if repo.findReplayCalls != 0 || repo.nextNumberCalls != 0 || repo.createCalls != 0 {
		t.Fatalf("invalid keys reached persistence: %#v", repo)
	}
}

func TestCustomerCreateAlwaysInheritsAuthenticatedOwner(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	principal := createTestPrincipal("tenant-a", "sales-a")
	principal.PrimaryOrgID = "sales-org-a"
	input := createTestRequest("creator-owner-key")
	input.OwnerUserID, input.OwnerOrgID = "other-user", "other-org"

	result, err := service.Create(auth.WithPrincipal(context.Background(), principal), input)
	if err != nil || result == nil || result.OwnerUserID != "sales-a" || result.OwnerOrgID != "sales-org-a" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if repo.resource == nil || repo.resource.OwnerUserID != "sales-a" || repo.resource.OwnerOrgID != "sales-org-a" {
		t.Fatalf("persisted owner=%#v", repo.resource)
	}
}

func TestCustomerCreateExactReplayReturnsOriginalMaskedResponseWithoutSideEffects(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	audits := &createAuditStub{}
	service := newCreateTestService(t, repo, audits)
	ctx := auth.WithPrincipal(context.Background(), createTestPrincipal("tenant-a", "user-a"))
	input := createTestRequest("create-key")
	first, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(ctx, createTestRequest(" create-key "))
	if err != nil || second.ID != first.ID || second.CustomerNo != first.CustomerNo {
		t.Fatalf("first=%#v second=%#v err=%v", first, second, err)
	}
	if repo.nextNumberCalls != 1 || repo.createCalls != 1 || repo.createReplayCalls != 1 || audits.writes != 1 {
		t.Fatalf("number=%d create=%d replay=%d audits=%d", repo.nextNumberCalls, repo.createCalls, repo.createReplayCalls, audits.writes)
	}
	encoded, _ := json.Marshal(repo.prior)
	for _, plaintext := range []string{"91310000TEST", "13800138000", "zhang@example.com"} {
		if string(encoded) != "" && containsString(string(encoded), plaintext) {
			t.Fatalf("replay record leaked %q: %s", plaintext, encoded)
		}
	}
}

func TestCustomerCreateReplayRemainsStableAfterLaterDuplicateCandidate(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	ctx := auth.WithPrincipal(context.Background(), createTestPrincipal("tenant-a", "user-a"))
	first, err := service.Create(ctx, createTestRequest("stable-key"))
	if err != nil {
		t.Fatal(err)
	}
	repo.duplicates = []DuplicateCandidate{{ID: 99, Name: "示例客户"}}
	replay, err := service.Create(ctx, createTestRequest("stable-key"))
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
}

func TestCustomerCreateReplayConflictsAcrossPayloadActorAndTenant(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	actor := createTestPrincipal("tenant-a", "user-a")
	ctx := auth.WithPrincipal(context.Background(), actor)
	if _, err := service.Create(ctx, createTestRequest("bound-key")); err != nil {
		t.Fatal(err)
	}
	changed := createTestRequest("bound-key")
	changed.Contacts[0].Phone = "13900139000"
	if result, err := service.Create(ctx, changed); result != nil || !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload result=%#v err=%v", result, err)
	}
	for _, principal := range []auth.Principal{createTestPrincipal("tenant-a", "user-b"), createTestPrincipal("tenant-b", "user-a")} {
		otherCtx := auth.WithPrincipal(context.Background(), principal)
		otherRepo := &createIdempotencyRepoStub{prior: repo.prior, resource: repo.resource, transactionErr: gorm.ErrDuplicatedKey}
		otherService := newCreateTestService(t, otherRepo, &createAuditStub{})
		result, err := otherService.Create(otherCtx, createTestRequest("bound-key"))
		if result != nil || !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Fatalf("principal=%#v result=%#v err=%v", principal, result, err)
		}
	}
}

func TestCustomerCreateRejectsUnauthorizedDuplicateOverrideBeforeReplayLookup(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	input := createTestRequest("override-key")
	input.DuplicateOverride = true
	input.DuplicateOverrideReason = "verified"
	result, err := service.Create(auth.WithPrincipal(context.Background(), createTestPrincipal("tenant-a", "user-a")), input)
	if result != nil || !errors.Is(err, apperror.ErrForbidden) || repo.findReplayCalls != 0 {
		t.Fatalf("result=%#v err=%v replay lookups=%d", result, err, repo.findReplayCalls)
	}
}

func TestCustomerCreateAllowsCatalogAuthorizedDuplicateOverride(t *testing.T) {
	repo := &createIdempotencyRepoStub{duplicates: []DuplicateCandidate{{ID: 88, Name: "示例客户"}}}
	service := newCreateTestService(t, repo, &createAuditStub{})
	input := createTestRequest("authorized-override-key")
	input.DuplicateOverride = true
	input.DuplicateOverrideReason = "已核验为不同法律主体"
	principal := createTestPrincipal("tenant-a", "director-a")
	principal.Permissions["customer.duplicate.override"] = struct{}{}
	result, err := service.Create(auth.WithPrincipal(context.Background(), principal), input)
	if err != nil || result == nil || repo.createCalls != 1 {
		t.Fatalf("result=%#v err=%v create_calls=%d", result, err, repo.createCalls)
	}
}

func TestCustomerCreateReplayRequiresCurrentCreatePermission(t *testing.T) {
	repo := &createIdempotencyRepoStub{}
	service := newCreateTestService(t, repo, &createAuditStub{})
	principal := createTestPrincipal("tenant-a", "user-a")
	ctx := auth.WithPrincipal(context.Background(), principal)
	if _, err := service.Create(ctx, createTestRequest("permission-key")); err != nil {
		t.Fatal(err)
	}
	principal.Permissions = map[string]struct{}{}
	result, err := service.Create(auth.WithPrincipal(context.Background(), principal), createTestRequest("permission-key"))
	if result != nil || !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCustomerCreateDuplicateKeyRaceAcceptsOnlyStrictBoundWinner(t *testing.T) {
	actor := createTestPrincipal("tenant-a", "user-a")
	input := inheritCreateOwner(normalizeCreateRequest(createTestRequest("race-key")), actor)
	serviceForHash := newCreateTestService(t, &createIdempotencyRepoStub{}, &createAuditStub{})
	hash, err := serviceForHash.createRequestHash(actor, input)
	if err != nil {
		t.Fatal(err)
	}
	winnerResponse := Response{ID: 81, CustomerNo: "KH202608010009", Name: input.Name, Status: StatusActive, Version: 1}
	encoded, _ := json.Marshal(winnerResponse)
	winner := &CreateIdempotency{TenantID: actor.TenantID, ActorID: actor.UserID, Key: input.IdempotencyKey, RequestHash: hash, CustomerID: 81, ResponseJSON: encoded}
	winner.Status = "COMPLETED"
	winner.ResponseHash = responseDigest(encoded)
	resource := &Customer{Model: database.Model{ID: 81, TenantID: actor.TenantID, CreatedBy: actor.UserID}, CustomerNo: winnerResponse.CustomerNo}
	repo := &createIdempotencyRepoStub{transactionErr: &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_customer_create_idempotency'"}, commitWinner: winner, commitResource: resource}
	service := newCreateTestService(t, repo, &createAuditStub{})
	result, err := service.Create(auth.WithPrincipal(context.Background(), actor), input)
	if err != nil || result.ID != 81 {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	foreign := *winner
	foreign.CustomerID = 82
	badRepo := &createIdempotencyRepoStub{transactionErr: &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uq_customer_create_idempotency'"}, commitWinner: &foreign, commitResource: resource}
	badService := newCreateTestService(t, badRepo, &createAuditStub{})
	if result, err = badService.Create(auth.WithPrincipal(context.Background(), actor), input); result != nil || !errors.Is(err, ErrCreateReplayInvalid) {
		t.Fatalf("foreign result=%#v err=%v", result, err)
	}
	if isCustomerCreateRaceCandidate(errors.New("duplicate key text only")) ||
		isCustomerCreateRaceCandidate(gorm.ErrDuplicatedKey) ||
		isCustomerCreateRaceCandidate(&mysqlDriver.MySQLError{Number: 1205}) ||
		isCustomerCreateRaceCandidate(&mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uk_customer_no'"}) {
		t.Fatal("non-1062 errors must not trigger race recovery")
	}
}

func TestCustomerCreateCreditUniqueRaceRecoversOnlyAValidatedWinner(t *testing.T) {
	actor := createTestPrincipal("tenant-a", "user-a")
	input := inheritCreateOwner(normalizeCreateRequest(createTestRequest("credit-race-key")), actor)
	serviceForHash := newCreateTestService(t, &createIdempotencyRepoStub{}, &createAuditStub{})
	hash, err := serviceForHash.createRequestHash(actor, input)
	if err != nil {
		t.Fatal(err)
	}
	winnerResponse := Response{ID: 91, CustomerNo: "KH202608010019", Name: input.Name, Status: StatusActive, Version: 1}
	encoded, _ := json.Marshal(winnerResponse)
	winner := &CreateIdempotency{
		TenantID: actor.TenantID, ActorID: actor.UserID, Key: input.IdempotencyKey,
		RequestHash: hash, CustomerID: winnerResponse.ID, Status: "COMPLETED",
		ResponseJSON: encoded, ResponseHash: responseDigest(encoded),
	}
	resource := &Customer{Model: database.Model{ID: winnerResponse.ID, TenantID: actor.TenantID, CreatedBy: actor.UserID}, CustomerNo: winnerResponse.CustomerNo}
	creditDuplicate := mapWriteError(&mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'uk_customer_credit'"})
	repo := &createIdempotencyRepoStub{transactionErr: creditDuplicate, commitWinner: winner, commitResource: resource}
	service := newCreateTestService(t, repo, &createAuditStub{})
	result, err := service.Create(auth.WithPrincipal(context.Background(), actor), input)
	if err != nil || result.ID != winnerResponse.ID {
		t.Fatalf("validated credit winner result=%#v err=%v", result, err)
	}

	// An unrelated credit-code conflict has no winner for this actor/key and
	// must remain the original business error rather than becoming a replay.
	unrelatedRepo := &createIdempotencyRepoStub{transactionErr: creditDuplicate}
	unrelatedService := newCreateTestService(t, unrelatedRepo, &createAuditStub{})
	if result, err = unrelatedService.Create(auth.WithPrincipal(context.Background(), actor), input); result != nil || !errors.Is(err, creditDuplicate) {
		t.Fatalf("unrelated credit conflict result=%#v err=%v", result, err)
	}
}

func TestCustomerCreateCanonicalHashPreservesContactOrderAndNormalizesWhitespace(t *testing.T) {
	service := newCreateTestService(t, &createIdempotencyRepoStub{}, &createAuditStub{})
	actor := createTestPrincipal("tenant-a", "user-a")
	base := createTestRequest("key")
	base.Contacts = append(base.Contacts, ContactInput{Name: "李四", Phone: "13900139000"})
	trimmed := normalizeCreateRequest(base)
	hashA, err := service.createRequestHash(actor, trimmed)
	if err != nil {
		t.Fatal(err)
	}
	hashB, _ := service.createRequestHash(actor, normalizeCreateRequest(base))
	if hashA != hashB {
		t.Fatal("whitespace normalization is unstable")
	}
	reversed := trimmed
	reversed.Contacts = []ContactInput{trimmed.Contacts[1], trimmed.Contacts[0]}
	hashC, _ := service.createRequestHash(actor, reversed)
	if hashA == hashC {
		t.Fatal("contact business order was omitted from the digest")
	}
}

func containsString(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

// Compile-time assertions keep this security stub aligned with both customer
// persistence boundaries even as the production repository evolves.
var _ Repository = (*createIdempotencyRepoStub)(nil)
var _ CreateRepository = (*createIdempotencyRepoStub)(nil)
