package localidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/reportsecurity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
)

type localFlowPlatform struct {
	provisions atomic.Int32
	failRole   atomic.Bool
}

// Provision 返回合成平台回执以隔离数据库测试，不冒充真实平台验收。
// 参数：上下文、原操作号及冻结联系人。
// 返回值：固定合成主体。
func (p *localFlowPlatform) Provision(_ context.Context, _ string, contact ContactInput) (PlatformIdentity, error) {
	p.provisions.Add(1)
	if contact.Phone == "13900000000" {
		return PlatformIdentity{PlatformUserID: "second-real-subject", AccountNo: "PU87654321"}, nil
	}
	return PlatformIdentity{PlatformUserID: "local-real-subject", AccountNo: "PU12345678"}, nil
}

// AssignRole 模拟未确认角色回执，验证原操作恢复。
// 参数：上下文、主体及原操作号。
// 返回值：受控依赖故障。
func (p *localFlowPlatform) AssignRole(context.Context, string, string) error {
	if p.failRole.Load() {
		return errors.New("unconfirmed role")
	}
	return nil
}

// BindCustomer 满足合成平台端口，真实HTTP合同另由适配器测试验证。
// 参数：上下文、主体、本地客户及原操作号。
// 返回值：无。
func (*localFlowPlatform) BindCustomer(context.Context, string, uint64, string) error { return nil }

// RevokeRole 满足精确停用测试端口。
// 参数：上下文、主体及原操作号。
// 返回值：无。
func (*localFlowPlatform) RevokeRole(context.Context, string, string) error { return nil }

// DisableBinding 满足精确停用测试端口。
// 参数：上下文、主体、客户及原操作号。
// 返回值：无。
func (*localFlowPlatform) DisableBinding(context.Context, string, uint64, string) error { return nil }

type flowClock struct{}

// Now 为合成用例提供当前服务器时钟。
// 参数：无。
// 返回值：UTC时钟。
func (flowClock) Now() time.Time { return time.Now().UTC() }

type flowFiles struct{ raw []byte }

// Upload 产生合成源描述符供真实扫描与vault测试，明确不是网关验收。
// 参数：ctx/request/op/name/content为真实本地持久坐标及PDF。
// 返回值：合成PORTAL_LOCAL网关描述符。
func (f *flowFiles) Upload(_ context.Context, _ uint64, _ string, name string, content io.Reader) (report.FileDescriptor, error) {
	raw, err := io.ReadAll(content)
	if err != nil {
		return report.FileDescriptor{}, err
	}
	f.raw = raw
	sum := sha256.Sum256(raw)
	return report.FileDescriptor{SourceType: "PORTAL_LOCAL", ObjectRef: "file-local-original", FileName: name, MIME: "application/pdf", FileHash: hex.EncodeToString(sum[:]), Size: int64(len(raw))}, nil
}

// OpenPortalReportFile 精确校验LOCAL来源后返回原PDF供真实摄取器处理。
// 参数：ctx/trace/file为作业坐标，input为冻结请求及摘要。
// 返回值：有界合成源流；来源不明确时失败。
func (f *flowFiles) OpenPortalReportFile(_ context.Context, _ string, file string, input filegatewayclient.PortalReportSourceInput) (filegatewayclient.PortalReportFile, error) {
	if input.SourceType != "PORTAL_LOCAL" || input.RequestID == 0 || file != "file-local-original" {
		return filegatewayclient.PortalReportFile{}, ErrNamespace
	}
	return filegatewayclient.PortalReportFile{Content: io.NopCloser(bytes.NewReader(f.raw)), VersionID: "immutable-local-version", SHA256: input.ExpectedSHA256, Size: int64(len(f.raw)), MIME: "application/pdf"}, nil
}

// flowPDF 创建真实可被严格解析的单页PDF，不使用业务文件。
// 参数：t为测试上下文。
// 返回值：原PDF字节。
func flowPDF(t *testing.T) []byte {
	t.Helper()
	old := model.ConfigPath
	model.ConfigPath = "disable"
	t.Cleanup(func() { model.ConfigPath = old })
	raw, _ := json.Marshal(map[string]any{"paper": "A4P", "pages": map[string]any{"1": map[string]any{"content": map[string]any{}}}})
	var out bytes.Buffer
	if err := api.Create(nil, bytes.NewReader(raw), &out, model.NewDefaultConfiguration()); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestLocalPortalPersistentRecoveryAndSecureReportMySQL 覆盖真实迁移、精确重试、ACL及安全摄取。
// 参数：t为显式隔离空库用例；外部平台和网关使用合成端口且另测HTTP合同。
// 返回值：无；失败阶段丢失、跨来源、重复任务、越权或提前签发时失败。
func TestLocalPortalPersistentRecoveryAndSecureReportMySQL(t *testing.T) {
	repo := foundationDB(t)
	platform := &localFlowPlatform{}
	platform.failRole.Store(true)
	protector, err := report.NewAESDescriptorProtector(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	accounts := account.NewService(account.NewGORMRepository(repo.db).UseLocalDirectory(), nil, nil, protector, flowClock{}, nil, "catalog", time.Hour)
	files := &flowFiles{}
	service, err := NewService(Dependencies{Repo: repo, Accounts: accounts, Protector: protector, DescriptorProtector: protector, HMACKey: bytes.Repeat([]byte{8}, 32), Platform: platform, Audit: requestaudit.NewPortalStore(repo.db), Mode: ModeLocal, TenantID: "local-tenant", EnvironmentCode: "test", PublicURL: "https://portal.test/customer-portal", CheckLicense: func(context.Context) error { return nil }, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	permissions := map[string]struct{}{}
	for _, code := range []string{PermissionCustomerManage, PermissionInviteManage, PermissionAccessDisable, PermissionProjectManage, PermissionReportManage} {
		permissions[code] = struct{}{}
	}
	ctx := requestctx.WithID(auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "local-tenant", UserID: "real-admin", Roles: []string{"portal_super_admin"}, Permissions: permissions, DataScopes: []auth.DataScope{{RoleCode: "portal_super_admin", ScopeType: "APPLICATION"}}}), "local-test-request")
	customer, err := service.CreateCustomer(ctx, CustomerInput{Code: "C-LOCAL", Name: "本地合成客户"})
	if err != nil {
		t.Fatal(err)
	}
	_, crossSourceErr := account.NewGORMRepository(repo.db).UpsertPendingLink(ctx, &account.IdentityLink{Model: database.Model{TenantID: "local-tenant", UpdatedAt: time.Now()}, CustomerID: customer.ID, PlatformUserID: "legacy-crm-subject", AccountNo: "legacy"})
	if apperror.As(crossSourceErr).Code != "PORTAL_CUSTOMER_AUTHORITY_CONFLICT" {
		t.Fatal("legacy CRM mapping crossed local authority", crossSourceErr)
	}
	contact, err := service.CreateContact(ctx, ContactInput{CustomerID: customer.ID, DisplayName: "合成联系人", Phone: "13800000000"})
	if err != nil {
		t.Fatal(err)
	}
	invitationInput := InvitationInput{CustomerID: customer.ID, ContactID: contact.ID, ExpectedContactVersion: contact.Version, IdempotencyKey: "original-invite"}
	first, err := service.CreateInvitation(ctx, invitationInput)
	if !errors.Is(err, ErrUnavailable) || first.Operation.Stage != "IDENTITY_READY" || first.Operation.Status != "RETRY_WAIT" {
		t.Fatalf("failure lost: %+v %v", first, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = service.CreateInvitation(ctx, invitationInput) }()
	}
	wg.Wait()
	var count int64
	if err := repo.db.Model(&Operation{}).Where("kind='INVITE'").Count(&count).Error; err != nil || count != 1 || platform.provisions.Load() != 1 {
		t.Fatalf("duplicate operation or identity: %d provisions=%d %v", count, platform.provisions.Load(), err)
	}
	platform.failRole.Store(false)
	invited, err := service.ResumeOperation(ctx, first.Operation.OperationNo)
	if err != nil || invited.Invitation == nil || invited.ActivationURL == "" {
		t.Fatalf("cannot resume original: %+v %v", invited, err)
	}
	changed := invitationInput
	changed.ExpectedContactVersion++
	if _, err := service.CreateInvitation(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("idempotency payload changed", err)
	}
	adapter := &AccountAdapter{Repo: repo, TenantID: "local-tenant", HMACKey: service.HMACKey}
	token := strings.TrimPrefix(invited.ActivationURL, service.PublicURL+"/activate?token=")
	verified, err := adapter.Verify(ctx, token)
	if err != nil || verified.CustomerID != customer.ID {
		t.Fatal(verified, err)
	}
	link, err := account.NewGORMRepository(repo.db).FindLink(ctx, "local-tenant", "local-real-subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := account.NewGORMRepository(repo.db).ActivateLink(tx, "local-tenant", link.ID, 3, link.PlatformUserID, time.Now()); err != nil {
			return err
		}
		return adapter.ConsumeInTransaction(tx, token, link.PlatformUserID)
	}); err != nil {
		t.Fatal("atomic invitation consumption", err)
	}
	projectInput := ProjectInput{CustomerID: customer.ID, Namespace: LocalNamespace, ProjectID: "P-LOCAL", Name: "门户自身项目", Status: "IN_PROGRESS", ProgressPct: 10, Stage: "已登记"}
	saved, err := service.SaveProject(ctx, projectInput)
	if err != nil || saved.ID == 0 {
		t.Fatal(saved, err)
	}
	projectRepo := project.NewGORMRepository(repo.db)
	scope := project.Scope{TenantID: "local-tenant", CustomerID: customer.ID, AccountID: link.PlatformUserID}
	if err := projectRepo.AssertVisible(ctx, scope, "P-LOCAL"); err == nil {
		t.Fatal("ungranted account inherited all projects")
	}
	grant, err := service.SetProjectAccess(ctx, ProjectAccess{CustomerID: customer.ID, Namespace: LocalNamespace, ProjectID: "P-LOCAL", AccountID: link.PlatformUserID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := projectRepo.AssertVisible(ctx, scope, "P-LOCAL"); err != nil {
		t.Fatal("exact grant invisible", err)
	}
	invalid := projectInput
	invalid.Namespace = ""
	if _, err := service.SaveProject(ctx, invalid); err == nil {
		t.Fatal("CRM namespace accepted")
	}
	unauthorized := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "local-tenant", UserID: link.PlatformUserID, Roles: []string{"portal_customer"}, Permissions: permissions})
	if _, err := service.CreateCustomer(unauthorized, CustomerInput{Code: "bad", Name: "bad"}); err == nil {
		t.Fatal("customer wrote admin directory")
	}
	_, crossSourceErr = projectRepo.UpsertBundle(ctx, &project.Bundle{Snapshot: project.Snapshot{Model: database.Model{TenantID: "local-tenant"}, CustomerID: customer.ID, ProjectID: "P-LOCAL", ProjectName: "wrong PMS projection", SourceUpdatedAt: time.Now().Add(time.Hour)}})
	if apperror.As(crossSourceErr).Code != "PORTAL_CUSTOMER_AUTHORITY_CONFLICT" {
		t.Fatal("old PMS projection crossed local authority", crossSourceErr)
	}
	secondContact, err := service.CreateContact(ctx, ContactInput{CustomerID: customer.ID, DisplayName: "第二真实测试账号", Phone: "13900000000"})
	if err != nil {
		t.Fatal(err)
	}
	secondInvite, err := service.CreateInvitation(ctx, InvitationInput{CustomerID: customer.ID, ContactID: secondContact.ID, ExpectedContactVersion: secondContact.Version, IdempotencyKey: "second-original-invite"})
	if err != nil {
		t.Fatal(err)
	}
	secondLink, err := account.NewGORMRepository(repo.db).FindLink(ctx, "local-tenant", "second-real-subject")
	if err != nil {
		t.Fatal(err)
	}
	secondToken := strings.TrimPrefix(secondInvite.ActivationURL, service.PublicURL+"/activate?token=")
	if err := repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := account.NewGORMRepository(repo.db).ActivateLink(tx, "local-tenant", secondLink.ID, 3, secondLink.PlatformUserID, time.Now()); err != nil {
			return err
		}
		return adapter.ConsumeInTransaction(tx, secondToken, secondLink.PlatformUserID)
	}); err != nil {
		t.Fatal(err)
	}
	customerContext := func(subject string) context.Context {
		return requestctx.WithID(auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "local-tenant", UserID: subject, Roles: []string{"portal_customer"}, Permissions: map[string]struct{}{"report.read": {}, "report.download": {}, "project.read": {}}, DataScopes: []auth.DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}}), "customer-download-fixture")
	}
	firstActor := report.Actor{TenantID: "local-tenant", CustomerID: customer.ID, AccountID: link.PlatformUserID}
	secondActor := report.Actor{TenantID: "local-tenant", CustomerID: customer.ID, AccountID: secondLink.PlatformUserID}
	firstCustomerCtx, secondCustomerCtx := customerContext(link.PlatformUserID), customerContext(secondLink.PlatformUserID)
	raw := flowPDF(t)
	reportInput := ReportInput{CustomerID: customer.ID, Namespace: LocalNamespace, ProjectID: "P-LOCAL", AccountID: link.PlatformUserID, ReportType: "PDF", Reason: "本地合成报告", FileName: "original.pdf", IdempotencyKey: "original-report"}
	created, err := service.CreateReport(ctx, reportInput, bytes.NewReader(raw))
	if err != nil || created.ReportRequest == nil || created.ReportRequest.Status != "INGEST_PENDING" {
		t.Fatalf("report not pending: %+v %v", created, err)
	}
	repeated, err := service.CreateReport(ctx, reportInput, bytes.NewReader(raw))
	if err != nil || repeated.Operation.OperationNo != created.Operation.OperationNo {
		t.Fatal("report replay changed", err)
	}
	if err := repo.db.Model(&report.IngestJob{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate ingest", count, err)
	}
	downloadService := report.NewDownloadService(report.NewGORMRepository(repo.db), nil, nil, flowClock{}, flowIDs{}, report.CryptoTokenGenerator{}, time.Hour).RequireProductionSecurity(nil).UseSourceAccess(func(ctx context.Context, actor report.Actor, id uint64) error {
		return service.AssertCustomerReport(ctx, actor, id, "report.download")
	})
	requestID := created.ReportRequest.RequestID
	visible, err := service.CustomerReports(secondCustomerCtx, secondActor, 1, 20)
	if err != nil || visible.Total != 0 {
		t.Fatal("second account saw local report list", visible.Total, err)
	}
	if err := service.AssertCustomerReport(secondCustomerCtx, secondActor, requestID, "report.read"); err == nil {
		t.Fatal("second account saw report detail")
	}
	if _, err := downloadService.CreateGrant(secondCustomerCtx, secondActor, requestID, report.GrantCommand{IdempotencyKey: "unauthorized"}); err == nil {
		t.Fatal("second account minted grant")
	}
	if _, err := downloadService.AuthorizeDownload(secondCustomerCtx, secondActor, requestID, strings.Repeat("x", 40), report.DownloadMetadata{}); err == nil {
		t.Fatal("second account downloaded source")
	}
	if _, err := downloadService.CreateGrant(firstCustomerCtx, firstActor, requestID, report.GrantCommand{IdempotencyKey: "before-ingest"}); !errors.Is(err, report.ErrReportNotIssued) {
		t.Fatal("unscanned report granted", err)
	}
	if _, err := downloadService.AuthorizeDownload(firstCustomerCtx, firstActor, requestID, strings.Repeat("x", 40), report.DownloadMetadata{}); err == nil {
		t.Fatal("unscanned report downloaded")
	}
	if _, err := service.SetProjectAccess(ctx, ProjectAccess{CustomerID: customer.ID, Namespace: LocalNamespace, ProjectID: "P-LOCAL", AccountID: secondLink.PlatformUserID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.AssertCustomerReport(secondCustomerCtx, secondActor, requestID, "report.download"); err == nil {
		t.Fatal("same project grant expanded report recipient")
	}
	callbackService := report.NewService(report.NewGORMRepository(repo.db), nil, nil, protector, flowClock{}, nil)
	crossSourceErr = callbackService.ApplyCallback(ctx, report.Callback{IdempotencyKey: "legacy-callback", TenantID: "local-tenant", CustomerID: customer.ID, RequestID: requestID, ProjectID: "P-LOCAL", Version: 1, Status: report.StatusApprovedProcessing, DownstreamRequestID: "real-legacy-pms-request", ApprovalResult: "旧来源审批"})
	if apperror.As(crossSourceErr).Code != "PORTAL_CUSTOMER_AUTHORITY_CONFLICT" {
		t.Fatal("legacy callback touched local request", crossSourceErr)
	}
	var job report.IngestJob
	if err := repo.db.First(&job).Error; err != nil {
		t.Fatal(err)
	}
	var descriptor report.FileDescriptor
	plain, err := protector.Decrypt(ctx, job.DescriptorCipher)
	if err != nil || json.Unmarshal(plain, &descriptor) != nil || descriptor.SourceType != "PORTAL_LOCAL" {
		t.Fatal("local source lost", err)
	}
	store, err := reportsecurity.NewEncryptedStore(t.TempDir(), bytes.Repeat([]byte{9}, 32), "local-tenant", protector)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ingestor, err := reportsecurity.NewGatewayIngestor(files, store)
	if err != nil {
		t.Fatal(err)
	}
	job.Status, job.LockedBy = report.IngestProcessing, "local-fixture-worker"
	if err := repo.db.Model(&job).Updates(map[string]any{"status": job.Status, "locked_by": job.LockedBy}).Error; err != nil {
		t.Fatal(err)
	}
	receipt, err := ingestor.IngestForJob(ctx, job, descriptor)
	if err != nil {
		t.Fatal("real scan/vault", err)
	}
	reportService := report.NewService(report.NewGORMRepository(repo.db), nil, nil, protector, flowClock{}, nil)
	if err := reportService.CompleteIngest(ctx, job, descriptor, receipt); err != nil {
		t.Fatal("trusted issuance", err)
	}
	if err := reportService.CompleteIngest(ctx, job, descriptor, receipt); err != nil {
		t.Fatal("trusted issuance replay", err)
	}
	var request report.Request
	if err := repo.db.First(&request, job.RequestID).Error; err != nil || request.Status != report.StatusIssued || request.DownstreamRequestID != "" {
		t.Fatal("invalid local issuance", request.Status, err)
	}
	identity, err := adapter.Resolve(ctx, "local-tenant", customer.ID, link.PlatformUserID)
	if err != nil || identity.CustomerName != customer.Name || !strings.Contains(identity.AccountMasked, "*") {
		t.Fatal("local watermark authority", identity, err)
	}
	if err := repo.db.Model(&report.Notification{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate issuance notification", count, err)
	}
	originalGrant, err := downloadService.CreateGrant(firstCustomerCtx, firstActor, requestID, report.GrantCommand{IdempotencyKey: "after-real-ingest"})
	if err != nil || originalGrant.DownloadToken == "" {
		t.Fatal("issued local report did not grant", err)
	}
	_, err = service.SetProjectAccess(ctx, ProjectAccess{CustomerID: customer.ID, Namespace: LocalNamespace, ProjectID: "P-LOCAL", AccountID: link.PlatformUserID, Enabled: false, ExpectedVersion: grant.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err := projectRepo.AssertVisible(ctx, scope, "P-LOCAL"); err == nil {
		t.Fatal("revoke returned legacy access")
	}
	if _, err := downloadService.AuthorizeDownload(firstCustomerCtx, firstActor, requestID, originalGrant.DownloadToken, report.DownloadMetadata{}); err == nil {
		t.Fatal("revoked MANUAL access reused old grant")
	}
	if rows, err := service.CustomerReports(firstCustomerCtx, firstActor, 1, 20); err != nil || rows.Total != 0 {
		t.Fatal("revoked account retained local report list", err)
	}

	link, err = account.NewGORMRepository(repo.db).FindLink(ctx, "local-tenant", link.PlatformUserID)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := service.DisableAccess(ctx, DisableInput{CustomerID: customer.ID, PlatformUserID: link.PlatformUserID, ExpectedVersion: link.Version, Reason: "合成用例停用", IdempotencyKey: "original-disable"})
	if err != nil || stopped.Operation.Status != "COMPLETED" {
		t.Fatal(stopped, err)
	}
	if err := adapter.ValidateCustomer(ctx, "local-tenant", customer.ID, link.PlatformUserID, false); err == nil {
		t.Fatal("disabled local identity still authorized")
	}
}

type flowIDs struct{}

// NewID 为真实下载grant生成独立测试随机坐标。
// 参数：无。
// 返回值：高熵请求编号。
func (flowIDs) NewID() string { return requestctx.NewID() }
