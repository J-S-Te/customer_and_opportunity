package customer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// provisionRepoStub 在交互式创建替身之上提供通道专用的候选查询能力。
// 嵌入 Repository 接口使未使用的方法保持 nil，任何越权调用都会立即暴露。
type provisionRepoStub struct {
	*createIdempotencyRepoStub
	candidates        []ProvisionCandidate
	candidatesErr     error
	tenantQueried     string
	normalizedQueried string
	candidateQueries  int
}

func (r *provisionRepoStub) FindProvisionCandidates(_ context.Context, tenantID, normalizedName string) ([]ProvisionCandidate, error) {
	r.candidateQueries++
	r.tenantQueried, r.normalizedQueried = tenantID, normalizedName
	return r.candidates, r.candidatesErr
}

// provisionOwnerCatalog 是可控的负责人目录替身，用来证明通道的服务端校验没有被跳过。
type provisionOwnerCatalog struct {
	page          ownerdirectory.Page
	listErr       error
	validateErr   error
	listCalls     int
	validateCalls int
	validatedUser string
	validatedOrg  string
}

func (c *provisionOwnerCatalog) List(_ context.Context, query ownerdirectory.Query) (ownerdirectory.Page, error) {
	c.listCalls++
	if query.UserID == "" {
		return ownerdirectory.Page{}, c.listErr
	}
	return c.page, c.listErr
}

func (c *provisionOwnerCatalog) Validate(_ context.Context, userID, organizationID string) error {
	c.validateCalls++
	c.validatedUser, c.validatedOrg = userID, organizationID
	return c.validateErr
}

func (c *provisionOwnerCatalog) Resolve(context.Context, []string) (map[string]ownerdirectory.User, error) {
	return nil, c.listErr
}

// provisionOwnerPage 返回“owner-x 的主组织是 org-a”的权威目录页。
func provisionOwnerPage() ownerdirectory.Page {
	return ownerdirectory.Page{Items: []ownerdirectory.User{{
		ID:            "owner-x",
		Organizations: []ownerdirectory.Organization{{ID: "org-a", Name: "销售一部", IsPrimary: true}},
	}}}
}

// provisionPrincipal 只授予通道 scope，用来证明普通 customer.create 不是进入条件。
func provisionPrincipal() auth.Principal {
	return auth.Principal{TenantID: "tenant-a", UserID: "machine:contract-system", DisplayName: "合同系统",
		Permissions: map[string]struct{}{ProvisionScope: {}}, ScopeMode: auth.ScopeAll}
}

func provisionCreateInput(key string) ProvisionCreateRequest {
	return ProvisionCreateRequest{
		Name:            " 历史客户甲 ",
		OwnerIdentityID: " owner-x ",
		ActorIdentityID: " actor-9 ",
		IdempotencyKey:  key,
		Contacts:        []ContactInput{{Name: " 张三 ", Phone: " 13800138000 ", Email: " zhang@example.com ", IsRegistration: true}},
	}
}

func provisionTestService(t *testing.T, repo *provisionRepoStub, writer *createAuditStub, catalog *provisionOwnerCatalog) *Service {
	t.Helper()
	service := NewService(nil, repo, writer, profileCodec(t)).UseFreezeChecker(allowAllFreezeChecker{})
	if catalog != nil {
		service = service.UseOwnerDirectory(catalog)
	}
	return service
}

func TestProvisionLookupMarksOnlySingleEnabledCandidateAsUnique(t *testing.T) {
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}}
	writer := &createAuditStub{}
	service := provisionTestService(t, repository, writer, nil)
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	repository.candidates = []ProvisionCandidate{{ID: 12, Name: "历史客户甲", Status: StatusActive, Enabled: true}}
	lookup, err := service.LookupProvisionCandidates(ctx, " 历史 客户甲 ")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	// 归一化必须复用交互式查重的 trim + 去空白 + 小写规则。
	if lookup.NormalizedName != "历史客户甲" || repository.normalizedQueried != "历史客户甲" || repository.tenantQueried != "tenant-a" {
		t.Fatalf("normalized=%q queried=%q tenant=%q", lookup.NormalizedName, repository.normalizedQueried, repository.tenantQueried)
	}
	if !lookup.Unique || len(lookup.Candidates) != 1 {
		t.Fatalf("unique=%t candidates=%#v", lookup.Unique, lookup.Candidates)
	}
	if writer.writes != 1 || writer.last.Operation != "IMPORT_PROVISION_LOOKUP" || !strings.Contains(string(writer.last.AfterJSON), ProvisionOrigin) {
		t.Fatalf("lookup audit=%#v", writer.last)
	}

	repository.candidates = []ProvisionCandidate{
		{ID: 12, Name: "历史客户甲", Status: StatusActive, Enabled: true},
		{ID: 13, Name: "历史客户甲", Status: StatusVoid, Enabled: false},
	}
	lookup, err = service.LookupProvisionCandidates(ctx, "历史客户甲")
	if err != nil || lookup.Unique || len(lookup.Candidates) != 2 {
		t.Fatalf("multi-hit lookup=%#v err=%v", lookup, err)
	}

	repository.candidates = []ProvisionCandidate{{ID: 14, Name: "历史客户甲", Status: StatusVoid, Enabled: false}}
	lookup, err = service.LookupProvisionCandidates(ctx, "历史客户甲")
	if err != nil || lookup.Unique {
		t.Fatalf("disabled hit must not be unique: %#v err=%v", lookup, err)
	}
}

func TestProvisionOwnerDirectoryRejectionIsUnprocessable(t *testing.T) {
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}}
	catalog := &provisionOwnerCatalog{page: provisionOwnerPage(), validateErr: ownerdirectory.ErrSelectionInvalid}
	service := provisionTestService(t, repository, &createAuditStub{}, catalog)
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	result, err := service.CreateProvision(ctx, provisionCreateInput("provision-owner"))
	if result != nil {
		t.Fatalf("rejected owner still created customer: %#v", result)
	}
	appErr := apperror.As(err)
	if appErr.HTTPStatus != 422 || appErr.Code != "CRM_IMPORT_PROVISION_OWNER_INVALID" {
		t.Fatalf("owner rejection status=%d code=%q err=%v", appErr.HTTPStatus, appErr.Code, err)
	}
	if catalog.validateCalls != 1 || catalog.validatedUser != "owner-x" || catalog.validatedOrg != "org-a" {
		t.Fatalf("directory not consulted: %#v", catalog)
	}
	if repository.createCalls != 0 || repository.nextNumberCalls != 0 {
		t.Fatalf("rejected owner reached persistence: %#v", repository)
	}
}

func TestProvisionCreateRejectsMachineIdentityAndMissingOwner(t *testing.T) {
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}}
	catalog := &provisionOwnerCatalog{page: provisionOwnerPage()}
	service := provisionTestService(t, repository, &createAuditStub{}, catalog)
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	for _, owner := range []string{"", "machine:contract-system"} {
		input := provisionCreateInput("provision-machine-owner")
		input.OwnerIdentityID = owner
		_, err := service.CreateProvision(ctx, input)
		appErr := apperror.As(err)
		if appErr.HTTPStatus != 422 || appErr.Code != "CRM_IMPORT_PROVISION_OWNER_INVALID" {
			t.Fatalf("owner=%q status=%d code=%q err=%v", owner, appErr.HTTPStatus, appErr.Code, err)
		}
	}
	if catalog.listCalls != 0 || catalog.validateCalls != 0 {
		t.Fatalf("机器/空负责人必须在校验前拒绝: %#v", catalog)
	}
}

func TestProvisionCreateRejectsExistingNameWithConflict(t *testing.T) {
	// 通道用归一化后的精确查询判重：同名活跃/停用客户都算已占用。
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}, candidates: []ProvisionCandidate{
		{ID: 12, Name: "历史客户甲", Status: StatusActive, Enabled: true},
		{ID: 13, Name: "历史客户甲", Status: StatusVoid, Enabled: false},
	}}
	service := provisionTestService(t, repository, &createAuditStub{}, &provisionOwnerCatalog{page: provisionOwnerPage()})
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	_, err := service.CreateProvision(ctx, provisionCreateInput("provision-duplicate"))
	appErr := apperror.As(err)
	if appErr.HTTPStatus != 409 || appErr.Code != "CRM_IMPORT_PROVISION_CUSTOMER_AMBIGUOUS" {
		t.Fatalf("duplicate status=%d code=%q err=%v", appErr.HTTPStatus, appErr.Code, err)
	}
	details, ok := appErr.Details.(provisionAmbiguityDetail)
	if !ok || len(details.Candidates) != 2 || details.Candidates[0].ID != 12 {
		t.Fatalf("candidate details=%#v", appErr.Details)
	}
	if repository.createCalls != 0 {
		t.Fatalf("duplicate name reached persistence: %#v", repository)
	}
}

func TestProvisionCreateFillsDefaultsAndBindsValidatedOwner(t *testing.T) {
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}}
	writer := &createAuditStub{}
	catalog := &provisionOwnerCatalog{page: provisionOwnerPage()}
	service := provisionTestService(t, repository, writer, catalog)
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	created, err := service.CreateProvision(ctx, provisionCreateInput("provision-create"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "历史客户甲" || created.CustomerType != DefaultProvisionCustomerType || created.Industry != DefaultProvisionIndustry || created.Region != DefaultProvisionRegion {
		t.Fatalf("defaults not applied: %#v", created)
	}
	if created.OwnerUserID != "owner-x" || created.OwnerOrgID != "org-a" {
		t.Fatalf("owner binding=%q/%q", created.OwnerUserID, created.OwnerOrgID)
	}
	if repository.resource == nil || repository.resource.CreatedBy != "actor-9" || repository.resource.UpdatedBy != "actor-9" {
		t.Fatalf("operator attribution=%#v", repository.resource)
	}
	if len(repository.resource.Contacts) != 1 || !repository.resource.Contacts[0].IsRegistration || repository.resource.Contacts[0].PhoneMasked == "" {
		t.Fatalf("contacts=%#v", repository.resource.Contacts)
	}
	if writer.last.Operation != "IMPORT_PROVISION_CREATE" || !strings.Contains(string(writer.last.AfterJSON), ProvisionOrigin) ||
		writer.last.ActorID != "machine:contract-system" {
		t.Fatalf("create audit=%#v", writer.last)
	}
	var snapshot provisionCustomerSnapshot
	if err = json.Unmarshal(writer.last.AfterJSON, &snapshot); err != nil || snapshot.Origin != ProvisionOrigin || snapshot.ActorIdentityID != "actor-9" {
		t.Fatalf("audit snapshot=%#v err=%v", snapshot, err)
	}
}

func TestProvisionCreateReplaysIdempotentRetryWithoutSecondWrite(t *testing.T) {
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}}
	writer := &createAuditStub{}
	service := provisionTestService(t, repository, writer, &provisionOwnerCatalog{page: provisionOwnerPage()})
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	first, err := service.CreateProvision(ctx, provisionCreateInput("provision-retry"))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := service.CreateProvision(ctx, provisionCreateInput("provision-retry"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID || first.CustomerNo != second.CustomerNo {
		t.Fatalf("replay=%#v first=%#v", second, first)
	}
	if repository.createCalls != 1 || repository.nextNumberCalls != 1 || writer.writes != 1 {
		t.Fatalf("replay wrote again: create=%d number=%d audit=%d", repository.createCalls, repository.nextNumberCalls, writer.writes)
	}

	// 同一幂等键换载荷必须被拒绝，避免合同侧用旧键提交另一条历史记录。
	changed := provisionCreateInput("provision-retry")
	changed.Name = "历史客户乙"
	if _, err = service.CreateProvision(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload error=%v", err)
	}
}

func TestProvisionCreateRequiresIdempotencyKeyAndScope(t *testing.T) {
	repository := &provisionRepoStub{createIdempotencyRepoStub: &createIdempotencyRepoStub{}}
	service := provisionTestService(t, repository, &createAuditStub{}, &provisionOwnerCatalog{page: provisionOwnerPage()})
	ctx := auth.WithPrincipal(context.Background(), provisionPrincipal())

	if _, err := service.CreateProvision(ctx, provisionCreateInput("   ")); !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("missing key error=%v", err)
	}

	// 普通创建权限不能进入本通道：scope 白名单独立于 customer.create。
	ordinary := auth.Principal{TenantID: "tenant-a", UserID: "user-a", Permissions: map[string]struct{}{"customer.create": {}}}
	if _, err := service.CreateProvision(auth.WithPrincipal(context.Background(), ordinary), provisionCreateInput("provision-scope")); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("scope check error=%v", err)
	}
	if _, err := service.LookupProvisionCandidates(auth.WithPrincipal(context.Background(), ordinary), "历史客户甲"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("lookup scope check error=%v", err)
	}
}

func TestProvisionLookupFailsClosedWhenDirectoryIsNotWired(t *testing.T) {
	service := NewService(nil, &createIdempotencyRepoStub{}, &createAuditStub{}, profileCodec(t))
	_, err := service.LookupProvisionCandidates(auth.WithPrincipal(context.Background(), provisionPrincipal()), "历史客户甲")
	if !errors.Is(err, ErrProvisionUnavailable) {
		t.Fatalf("unwired directory error=%v", err)
	}
}
