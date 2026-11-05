package opportunity

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// provisionOwnerCatalog 是可控的负责人目录替身，用来证明通道没有跳过服务端校验。
type provisionOwnerCatalog struct {
	page          ownerdirectory.Page
	listErr       error
	validateErr   error
	listCalls     int
	validateCalls int
	validatedUser string
	validatedOrg  string
}

func (c *provisionOwnerCatalog) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	c.listCalls++
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

func provisionOwnerPage() ownerdirectory.Page {
	return ownerdirectory.Page{Items: []ownerdirectory.User{{
		ID:            "owner-x",
		Organizations: []ownerdirectory.Organization{{ID: "org-a", Name: "销售一部", IsPrimary: true}},
	}}}
}

// provisionContractVerifier 模拟已配置的合同验真依赖。
type provisionContractVerifier struct {
	belongs bool
	err     error
	calls   int
}

func (v *provisionContractVerifier) BelongsToCustomer(context.Context, string, uint64) (bool, error) {
	v.calls++
	return v.belongs, v.err
}

// provisionPrincipal 只授予通道 scope；普通 opportunity.create 不是进入条件。
func provisionPrincipal() auth.Principal {
	return auth.Principal{TenantID: "tenant-a", UserID: "machine:contract-system", DisplayName: "合同系统",
		Permissions: map[string]struct{}{ProvisionScope: {}}, ScopeMode: auth.ScopeAll}
}

func provisionOpportunityInput(key string) ProvisionCreateRequest {
	return ProvisionCreateRequest{
		Name: " 历史商机甲 ", CustomerID: 3, ExpectedAmount: "100.00", ExpectedSignDate: "2020-03-01",
		RequirementSummary: " 历史合同要求 ", OwnerIdentityID: " owner-x ", ActorIdentityID: " actor-9 ",
		IdempotencyKey: key,
	}
}

func provisionCreateService(repo Repository, writer audit.Writer, catalog ownerdirectory.Catalog) *Service {
	service := createTestService(repo, writer)
	if catalog != nil {
		service = service.UseOwnerDirectory(catalog)
	}
	return service
}

// provisionDryRunDB 提供不连业务库的 GORM 句柄：阶段写入路径需要真实事务边界，
// 因此用内存驱动承担 BEGIN/COMMIT，SQL 一律空实现，绝不访问数据库。
func provisionDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(provisionStubConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, DryRun: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// provisionStubConnector 装配内存驱动，避免单测依赖真实 MySQL。
type provisionStubConnector struct{}

func (provisionStubConnector) Connect(context.Context) (driver.Conn, error) {
	return provisionStubConnection{}, nil
}

func (provisionStubConnector) Driver() driver.Driver { return provisionStubDriver{} }

type provisionStubDriver struct{}

func (provisionStubDriver) Open(string) (driver.Conn, error) { return provisionStubConnection{}, nil }

type provisionStubConnection struct{}

func (provisionStubConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (provisionStubConnection) Close() error                        { return nil }
func (provisionStubConnection) Begin() (driver.Tx, error)           { return provisionStubTx{}, nil }

func (provisionStubConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return provisionStubTx{}, nil
}

func (provisionStubConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return provisionStubResult{}, nil
}

func (provisionStubConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return provisionStubRows{}, nil
}

type provisionStubTx struct{}

func (provisionStubTx) Commit() error   { return nil }
func (provisionStubTx) Rollback() error { return nil }

type provisionStubResult struct{}

func (provisionStubResult) LastInsertId() (int64, error) { return 1, nil }
func (provisionStubResult) RowsAffected() (int64, error) { return 1, nil }

type provisionStubRows struct{}

func (provisionStubRows) Columns() []string         { return []string{"id"} }
func (provisionStubRows) Close() error              { return nil }
func (provisionStubRows) Next([]driver.Value) error { return io.EOF }

func provisionSignService(t *testing.T, repo Repository, writer audit.Writer) *Service {
	t.Helper()
	return &Service{
		repo: repo, audit: writer, db: provisionDryRunDB(t), freeze: allowAllFreezeChecker{},
		now:               func() time.Time { return time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC) },
		createTransaction: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
	}
}

func TestProvisionOpportunityCreateAllowsPastSignDateOnlyOnProvisionChannel(t *testing.T) {
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	catalog := &provisionOwnerCatalog{page: provisionOwnerPage()}
	service := provisionCreateService(repo, &countingAuditWriter{}, catalog)
	ctx := createTestContext(provisionPrincipal())

	created, err := service.CreateProvision(ctx, provisionOpportunityInput("provision-opportunity"))
	if err != nil {
		t.Fatalf("provision create: %v", err)
	}
	if created.CurrentStage != StageInitial || created.Status != StatusFollowing {
		t.Fatalf("provision 创建必须停在初步接触 + FOLLOWING: %#v", created)
	}
	if created.Type != DefaultProvisionOpportunityType || created.Source != DefaultProvisionOpportunitySource {
		t.Fatalf("type/source 缺省未填「历史合同导入」: %#v", created)
	}
	if created.ExpectedSignDate != "2020-03-01" {
		t.Fatalf("历史日期被改写: %q", created.ExpectedSignDate)
	}
	if repo.resource == nil || repo.resource.OwnerUserID != "owner-x" || repo.resource.OwnerOrgID != "org-a" || repo.resource.CreatedBy != "actor-9" {
		t.Fatalf("负责人/操作者绑定=%#v", repo.resource)
	}

	// 普通创建仍必须拒绝过去的预计签约日：通道差异不得外溢到既有端点。
	_, ordinary := service.Create(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}),
		CreateRequest{Name: "普通商机", CustomerID: 3, Type: "软件", Source: "转介绍", ExpectedAmount: "100.00",
			ExpectedSignDate: "2020-03-01", RequirementSummary: "需求", IdempotencyKey: "ordinary-key"})
	if appErr := apperror.As(ordinary); appErr.Code != "CRM_OPPORTUNITY_INVALID_SIGN_DATE" {
		t.Fatalf("普通创建未拒绝历史日期: code=%q err=%v", appErr.Code, ordinary)
	}
}

func TestProvisionOpportunityRejectsOwnerFailingDirectoryValidation(t *testing.T) {
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	catalog := &provisionOwnerCatalog{page: provisionOwnerPage(), validateErr: ownerdirectory.ErrSelectionInvalid}
	service := provisionCreateService(repo, &countingAuditWriter{}, catalog)
	ctx := createTestContext(provisionPrincipal())

	result, err := service.CreateProvision(ctx, provisionOpportunityInput("provision-owner"))
	if result != nil {
		t.Fatalf("目录校验失败仍创建商机: %#v", result)
	}
	appErr := apperror.As(err)
	if appErr.HTTPStatus != 422 || appErr.Code != "CRM_IMPORT_PROVISION_OWNER_INVALID" {
		t.Fatalf("owner 校验失败 status=%d code=%q err=%v", appErr.HTTPStatus, appErr.Code, err)
	}
	if catalog.validateCalls != 1 || catalog.validatedUser != "owner-x" || catalog.validatedOrg != "org-a" {
		t.Fatalf("目录未被调用: %#v", catalog)
	}
	if repo.created != 0 || repo.numbered != 0 {
		t.Fatalf("owner 校验失败仍落库: created=%d numbered=%d", repo.created, repo.numbered)
	}
}

func TestProvisionOpportunityCreateRequiresIdempotencyKeyAndScope(t *testing.T) {
	repo := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
	service := provisionCreateService(repo, &countingAuditWriter{}, &provisionOwnerCatalog{page: provisionOwnerPage()})
	ctx := createTestContext(provisionPrincipal())

	if _, err := service.CreateProvision(ctx, provisionOpportunityInput("  ")); !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("missing key error=%v", err)
	}
	ordinary := auth.Principal{TenantID: "tenant-a", UserID: "user-a", Permissions: map[string]struct{}{"opportunity.create": {}}}
	if _, err := service.CreateProvision(createTestContext(ordinary), provisionOpportunityInput("provision-scope")); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("scope check error=%v", err)
	}
	if _, _, err := service.SignProvision(createTestContext(ordinary), 7, ProvisionSignRequest{ContractRef: "HT-1", ExpectedVersion: 7, Reason: "推进", ActorIdentityID: "actor-9"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("sign scope check error=%v", err)
	}
}

// followingOpportunity 返回通道创建后的商机：初步接触 + FOLLOWING，等待合同侧推进。
func followingOpportunity(version uint64) *Opportunity {
	value := &Opportunity{OpportunityNo: "SJ202608010001", CustomerID: 3, ExpectedAmount: decimal.RequireFromString("100.00"),
		CurrentStage: StageInitial, Status: StatusFollowing, TerminalPendingType: PendingNone, ContractLinkStatus: "PENDING"}
	value.ID, value.TenantID, value.Version, value.CreatedBy = 7, "tenant-a", version, "actor-9"
	return value
}

func TestProvisionSignIsIdempotentAndNeverEmitsSignedEvent(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: followingOpportunity(7)}
	writer := &auditCapture{}
	service := provisionSignService(t, repo, writer)
	verifier := &provisionContractVerifier{belongs: true}
	service.contracts = verifier
	ctx := createTestContext(provisionPrincipal())

	signed, replayed, err := service.SignProvision(ctx, 7, ProvisionSignRequest{ContractRef: "HT-1", ContractID: "01JCONTRACT", ExpectedVersion: 7, Reason: "历史合同已入库", ActorIdentityID: "actor-9"})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if replayed || signed.CurrentStage != StageSigned || signed.Status != StatusClosed || signed.ContractRef == nil || *signed.ContractRef != "HT-1" || signed.TerminalPendingType != PendingNone {
		t.Fatalf("终态投影=%#v replayed=%t", signed, replayed)
	}
	if verifier.calls != 1 {
		t.Fatalf("未走合同验真前置: calls=%d", verifier.calls)
	}
	if repo.stageUpdates != 1 || repo.stageLogs != 1 {
		t.Fatalf("阶段写入次数 updates=%d logs=%d", repo.stageUpdates, repo.stageLogs)
	}
	// 本通道创建与推进都不得投递 OPPORTUNITY_SIGNED，避免回流合同系统。
	if repo.outboxCreates != 0 {
		t.Fatalf("通道推进写了发件箱事件: %d", repo.outboxCreates)
	}
	if writer.operation != "IMPORT_PROVISION_SIGN" || writer.writes != 2 {
		t.Fatalf("推进审计 operation=%q writes=%d", writer.operation, writer.writes)
	}

	// 同 contract_ref 重复调用：返回既有结果，不重复写阶段日志、不重复验真。
	replayedSigned, replayed, err := service.SignProvision(ctx, 7, ProvisionSignRequest{ContractRef: "HT-1", ExpectedVersion: 7, Reason: "重复调用", ActorIdentityID: "actor-9"})
	if err != nil || !replayed {
		t.Fatalf("replay err=%v replayed=%t", err, replayed)
	}
	if replayedSigned.ID != signed.ID || replayedSigned.CurrentStage != StageSigned {
		t.Fatalf("replay 结果=%#v", replayedSigned)
	}
	if repo.stageUpdates != 1 || repo.stageLogs != 1 || verifier.calls != 1 || repo.outboxCreates != 0 {
		t.Fatalf("replay 重复写入: updates=%d logs=%d verifier=%d outbox=%d", repo.stageUpdates, repo.stageLogs, verifier.calls, repo.outboxCreates)
	}
}

func TestProvisionSignWithoutContractVerificationReturnsActionableUnavailable(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: followingOpportunity(7)}
	service := provisionSignService(t, repo, &auditCapture{})
	// 未配置合同验真依赖：必须给可操作的 503，而不是绕过验真前置。
	_, replayed, err := service.SignProvision(createTestContext(provisionPrincipal()), 7,
		ProvisionSignRequest{ContractRef: "HT-1", ExpectedVersion: 7, Reason: "推进", ActorIdentityID: "actor-9"})
	if replayed {
		t.Fatal("失败路径不应标记为幂等回放")
	}
	appErr := apperror.As(err)
	if appErr.HTTPStatus != 503 || appErr.Code != "CRM_IMPORT_PROVISION_CONTRACT_NOT_VERIFIED" {
		t.Fatalf("status=%d code=%q err=%v", appErr.HTTPStatus, appErr.Code, err)
	}
	if repo.stageUpdates != 0 || repo.stageLogs != 0 || repo.outboxCreates != 0 {
		t.Fatalf("未验真仍推进阶段: %#v", repo)
	}
}

func TestProvisionSignRejectsContractOfAnotherCustomer(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: followingOpportunity(7)}
	service := provisionSignService(t, repo, &auditCapture{})
	verifier := &provisionContractVerifier{belongs: false}
	service.contracts = verifier
	_, replayed, err := service.SignProvision(createTestContext(provisionPrincipal()), 7,
		ProvisionSignRequest{ContractRef: "HT-1", ExpectedVersion: 7, Reason: "推进", ActorIdentityID: "actor-9"})
	if replayed {
		t.Fatal("失败路径不应标记为幂等回放")
	}
	appErr := apperror.As(err)
	if appErr.HTTPStatus != 422 || appErr.Code != "CRM_OPPORTUNITY_CONTRACT_CUSTOMER_MISMATCH" {
		t.Fatalf("合同不属于该客户 status=%d code=%q err=%v", appErr.HTTPStatus, appErr.Code, err)
	}
	if verifier.calls != 1 {
		t.Fatalf("未调用合同验真: calls=%d", verifier.calls)
	}
	if repo.stageUpdates != 0 || repo.stageLogs != 0 || repo.outboxCreates != 0 {
		t.Fatalf("合同不匹配仍推进阶段: %#v", repo)
	}
}

func TestProvisionSignContractVerificationUnavailableReturns503(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: followingOpportunity(7)}
	service := provisionSignService(t, repo, &auditCapture{})
	service.contracts = &provisionContractVerifier{err: errors.New("contract service down")}
	_, replayed, err := service.SignProvision(createTestContext(provisionPrincipal()), 7,
		ProvisionSignRequest{ContractRef: "HT-1", ExpectedVersion: 7, Reason: "推进", ActorIdentityID: "actor-9"})
	if replayed {
		t.Fatal("失败路径不应标记为幂等回放")
	}
	appErr := apperror.As(err)
	if appErr.HTTPStatus != 503 || appErr.Code != "CRM_IMPORT_PROVISION_CONTRACT_NOT_VERIFIED" {
		t.Fatalf("验真依赖不可用 status=%d code=%q err=%v", appErr.HTTPStatus, appErr.Code, err)
	}
	if repo.stageUpdates != 0 || repo.stageLogs != 0 || repo.outboxCreates != 0 {
		t.Fatalf("验真依赖不可用仍推进阶段: %#v", repo)
	}
}

func TestProvisionSignRespectsVersionAndStageStateMachine(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: followingOpportunity(7)}
	service := provisionSignService(t, repo, &auditCapture{})
	service.contracts = &provisionContractVerifier{belongs: true}
	ctx := createTestContext(provisionPrincipal())

	if _, _, err := service.SignProvision(ctx, 7, ProvisionSignRequest{ContractRef: "HT-1", ExpectedVersion: 6, Reason: "版本过期", ActorIdentityID: "actor-9"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("版本冲突 error=%v", err)
	}
	if repo.stageUpdates != 0 {
		t.Fatalf("版本冲突仍推进: %d", repo.stageUpdates)
	}

	closed := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(7)}
	closedService := provisionSignService(t, closed, &auditCapture{})
	closedService.contracts = &provisionContractVerifier{belongs: true}
	// 已签约商机只能用同一 contract_ref 幂等回放，换合同必须走受控流程。
	_, _, err := closedService.SignProvision(ctx, 7, ProvisionSignRequest{ContractRef: "HT-2", ExpectedVersion: 7, Reason: "换合同", ActorIdentityID: "actor-9"})
	if !errors.Is(err, ErrTerminalStageLocked) {
		t.Fatalf("终态迁移 error=%v", err)
	}
	if closed.stageUpdates != 0 {
		t.Fatalf("终态仍推进: %d", closed.stageUpdates)
	}
}
