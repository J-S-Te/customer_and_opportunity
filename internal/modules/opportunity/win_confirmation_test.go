package opportunity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type winTestRepository struct{ *externalEdgesRepository }

// UpdateTerminalTodo 模拟已有乐观锁仓储的成功补全结果。
// 参数：ctx 为事务上下文，model 为商机，version 为期望版本。
// 返回值：无错误，并保留服务实际写入的字段。
func (r *winTestRepository) UpdateTerminalTodo(ctx context.Context, model *Opportunity, version uint64) error {
	model.Version = version + 1
	r.opportunity = model
	return nil
}

type winEvidenceMemory struct {
	saved *WinConfirmation
	err   error
	gets  int
}

// Create 保存服务产生的证据供业务断言读取。
// 参数：ctx 为事务上下文，value 为服务产生的证据。
// 返回值：注入的持久化错误或成功。
func (m *winEvidenceMemory) Create(ctx context.Context, value *WinConfirmation) error {
	m.saved = value
	return m.err
}

// Get 返回当前测试的证据记录。
// 参数：ctx 为请求上下文，tenant 为租户，id 为商机。
// 返回值：当前记录及注入错误。
func (m *winEvidenceMemory) Get(ctx context.Context, tenant string, id uint64) (*WinConfirmation, error) {
	m.gets++
	return m.saved, m.err
}

// localWinInput 提供明确的人工客户确认依据。
// 参数：无。
// 返回值：日期及金额均合法的证据输入。
func localWinInput() *WinEvidenceInput {
	return &WinEvidenceInput{EvidenceType: "CUSTOMER_CONFIRMATION", EvidenceReference: "客户采购确认邮件-7",
		ConfirmedOn: "2026-08-01", ConfirmedAmount: "100.00", EvidenceNote: "客户书面确认采购，原始邮件由销售保留"}
}

// localWinService 构造保留真实 GORM 查询生成的无网络服务编排测试。
// 参数：t 为当前测试，store 为证据存储端口替身。
// 返回值：使用乐观锁仓储替身及无实际 SQL 执行的服务。
func localWinService(t *testing.T, store WinEvidenceStore) (*Service, *winTestRepository) {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "test:test@tcp(127.0.0.1:1)/unused", SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, DryRun: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	model := signedOpportunity(7)
	model.CurrentStage, model.Status, model.ContractRef = StageRequirement, StatusFollowing, nil
	repo := &winTestRepository{externalEdgesRepository: &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: model}}
	service := externalTestService(repo)
	service.db = db
	service.createTransaction = func(ctx context.Context, operation func(context.Context) error) error { return operation(ctx) }
	service.UseWinConfirmation(WinModeLocalEvidence, false, store)
	return service, repo
}

// TestLocalWinCompletesWithoutContractAndStoresEvidence 验证独立 CRM 不伪造合同。
// 参数：t 为当前测试上下文。
// 返回值：无；缺证据、错误阶段或伪造合同引用时测试失败。
func TestLocalWinCompletesWithoutContractAndStoresEvidence(t *testing.T) {
	store := &winEvidenceMemory{}
	service, repo := localWinService(t, store)
	ctx := createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	result, err := service.ChangeStage(ctx, 7, StageChangeRequest{TargetStage: StageSigned,
		Version: 7, Reason: "客户确认采购", WinEvidence: localWinInput()})
	if err != nil {
		t.Fatal(err)
	}
	if result.CurrentStage != StageSigned || result.Status != StatusClosed || result.TerminalPendingType != PendingNone {
		t.Fatalf("赢单未闭环: %+v", result)
	}
	if result.ContractRef != nil || result.ContractID != nil || repo.outboxCreates != 0 {
		t.Fatal("本地赢单伪造合同或写入合同队列")
	}
	if store.saved == nil || store.saved.TenantID != "tenant-a" || store.saved.CustomerID != 3 || store.saved.ConfirmedBy != "actor-a" ||
		!store.saved.ConfirmedAmount.Equal(decimal.NewFromInt(100)) || store.saved.OpportunityVersion != 8 {
		t.Fatalf("证据未按服务端权威保存: %+v", store.saved)
	}
}

// TestUnsubscribedTransferNeverCreatesPendingEvent 验证已签约历史记录不能绕过停订阅。
// 参数：t 为当前测试上下文。
// 返回值：无；创建任何待投递事件时测试失败。
func TestUnsubscribedTransferNeverCreatesPendingEvent(t *testing.T) {
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: signedOpportunity(7)}
	service := externalTestService(repo).UseWinConfirmation(WinModeContract, false, nil)
	_, err := service.ContractTransfer(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
		ContractTransferRequest{Version: 7, Reason: "转合同", IdempotencyKey: "key"})
	if err == nil || repo.outboxCreates != 0 {
		t.Fatal("未订阅仍创建待投递事件")
	}
}

// TestLocalWinRejectsInvalidOrMixedEvidence 验证模式、金额、日期及字段边界。
// 参数：t 为当前测试上下文。
// 返回值：无；非法输入被放行时测试失败。
func TestLocalWinRejectsInvalidOrMixedEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WinEvidenceInput)
	}{
		{"missing reference", func(v *WinEvidenceInput) { v.EvidenceReference = " " }},
		{"missing note", func(v *WinEvidenceInput) { v.EvidenceNote = "" }},
		{"unknown type", func(v *WinEvidenceInput) { v.EvidenceType = "CONTRACT_VERIFIED" }},
		{"future day", func(v *WinEvidenceInput) { v.ConfirmedOn = "2026-08-02" }},
		{"invalid day", func(v *WinEvidenceInput) { v.ConfirmedOn = "2026-02-30" }},
		{"negative amount", func(v *WinEvidenceInput) { v.ConfirmedAmount = "-1" }},
		{"fractional cent", func(v *WinEvidenceInput) { v.ConfirmedAmount = "1.001" }},
		{"huge exponent", func(v *WinEvidenceInput) { v.ConfirmedAmount = "1e999999999" }},
		{"overflow", func(v *WinEvidenceInput) { v.ConfirmedAmount = "10000000000000000" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &winEvidenceMemory{}
			service, _ := localWinService(t, store)
			input := localWinInput()
			test.mutate(input)
			_, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
				StageChangeRequest{Version: 7, TargetStage: StageSigned, Reason: "确认采购", WinEvidence: input})
			if err == nil || store.saved != nil {
				t.Fatal("非法赢单证据被写入")
			}
		})
	}
	service, _ := localWinService(t, &winEvidenceMemory{})
	reference := "HT-1"
	_, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
		StageChangeRequest{Version: 7, TargetStage: StageSigned, Reason: "确认采购", WinEvidence: localWinInput(), ContractRef: &reference})
	if !errors.Is(err, ErrWinModeMismatch) {
		t.Fatalf("合同引用混入本地模式: %v", err)
	}
}

// TestContractModeStillRequiresRealVerification 验证默认合同模式没有被本地证据绕过。
// 参数：t 为当前测试上下文。
// 返回值：无；无合同适配器或混用证据被放行时失败。
func TestContractModeStillRequiresRealVerification(t *testing.T) {
	service, repo := localWinService(t, &winEvidenceMemory{})
	service.winMode = ""
	reference := "HT-1"
	ctx := createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	_, err := service.ChangeStage(ctx, 7, StageChangeRequest{Version: 7, TargetStage: StageSigned, Reason: "合同验真", ContractRef: &reference})
	if err == nil || repo.stageUpdates != 0 {
		t.Fatal("缺合同验真适配器仍写入阶段")
	}
	_, err = service.ChangeStage(ctx, 7, StageChangeRequest{Version: 7, TargetStage: StageSigned, Reason: "合同验真", WinEvidence: localWinInput()})
	if !errors.Is(err, ErrWinModeMismatch) {
		t.Fatalf("本地证据绕过合同模式: %v", err)
	}
}

// TestLocalWinEvidenceFailureDoesNotReportSuccess 验证存储失败沿事务向上传播。
// 参数：t 为当前测试上下文。
// 返回值：无；失败被吞或仍写成功审计时失败。
func TestLocalWinEvidenceFailureDoesNotReportSuccess(t *testing.T) {
	failure := errors.New("evidence storage failed")
	service, _ := localWinService(t, &winEvidenceMemory{err: failure})
	result, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
		StageChangeRequest{Version: 7, TargetStage: StageSigned, Reason: "确认采购", WinEvidence: localWinInput()})
	if result != nil || !errors.Is(err, failure) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if service.audit.(*countingAuditWriter).writes != 0 {
		t.Fatal("持久化失败仍写成功审计")
	}
}

// TestPendingLocalWinCompletesWithEvidence 验证外部中标的证据待办可独立补全。
// 参数：t 为当前测试上下文。
// 返回值：无；未清待办或伪造合同关联时失败。
func TestPendingLocalWinCompletesWithEvidence(t *testing.T) {
	store := &winEvidenceMemory{}
	service, repo := localWinService(t, store)
	repo.opportunity.CurrentStage, repo.opportunity.Status, repo.opportunity.TerminalPendingType = StageSigned, StatusClosed, PendingWinEvidence
	result, err := service.CompleteTerminalTodo(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
		TerminalTodoRequest{Version: 7, Reason: "补录客户依据", WinEvidence: localWinInput()})
	if err != nil || result.TerminalPendingType != PendingNone || result.ContractRef != nil || store.saved == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// TestWinEvidenceUsesBeijingDayBoundary 验证 UTC 当天傍晚已属于北京次日。
// 参数：t 为当前测试上下文。
// 返回值：无；合法北京日期被误判未来时失败。
func TestWinEvidenceUsesBeijingDayBoundary(t *testing.T) {
	input := localWinInput()
	input.ConfirmedOn = "2026-08-02"
	if _, err := validateWinEvidence(input, time.Date(2026, 8, 1, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

// TestWinConfirmationReadHonorsVisibilityAndRetainsHistory 验证先校验商机范围再读取证据。
// 参数：t 为当前测试上下文。
// 返回值：无；越权读或模式切换抹除历史时失败。
func TestWinConfirmationReadHonorsVisibilityAndRetainsHistory(t *testing.T) {
	store := &winEvidenceMemory{saved: &WinConfirmation{Mode: WinModeLocalEvidence, ConfirmedOn: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}}
	service, repo := localWinService(t, store)
	ctx := createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"})
	service.winMode = WinModeContract
	result, err := service.GetWinConfirmation(ctx, 7)
	if err != nil || result.Mode != WinModeContract || result.Confirmation == nil || result.ConfirmedOn != "2026-08-01" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store.gets = 0
	repo.opportunity = nil
	if _, err = service.GetWinConfirmation(ctx, 7); !errors.Is(err, ErrNotFound) || store.gets != 0 {
		t.Fatalf("不可见商机仍读证据: %v", err)
	}
}

// TestWinConfirmationRejectsStaleVersionAndClosedRewrite 验证证据不会被旧版本或终态改写。
// 参数：t 为当前测试上下文。
// 返回值：无；发生证据写入时测试失败。
func TestWinConfirmationRejectsStaleVersionAndClosedRewrite(t *testing.T) {
	for _, closed := range []bool{false, true} {
		store := &winEvidenceMemory{}
		service, repo := localWinService(t, store)
		version := uint64(6)
		if closed {
			version = 7
			repo.opportunity.CurrentStage, repo.opportunity.Status = StageSigned, StatusClosed
		}
		_, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
			StageChangeRequest{Version: version, TargetStage: StageSigned, Reason: "确认采购", WinEvidence: localWinInput()})
		if err == nil || store.saved != nil {
			t.Fatal("旧版本或终态改写了证据")
		}
	}
}

// TestContractModeVerifiedWinRemainsCompatible 验证默认模式仍可通过真实合同校验端口完成赢单。
// 参数：t 为当前测试上下文。
// 返回值：无；误写本地证据或丢失合同引用时失败。
func TestContractModeVerifiedWinRemainsCompatible(t *testing.T) {
	store := &winEvidenceMemory{}
	service, _ := localWinService(t, store)
	service.winMode = ""
	service.contracts = contractVerifierStub{belongs: true}
	reference := "HT-1"
	result, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7,
		StageChangeRequest{Version: 7, TargetStage: StageSigned, Reason: "合同确认", ContractRef: &reference})
	if err != nil || result.ContractRef == nil || *result.ContractRef != "HT-1" || store.saved != nil {
		t.Fatalf("合同模式兼容性异常: result=%+v err=%v", result, err)
	}
}

// TestExternalWinRequiresLocalEvidenceAndRetainsOriginalSnapshot 验证回调不能代替人工证据。
// 参数：t 为当前测试上下文。
// 返回值：无；自动确认、污染合同引用或破坏精确重放时失败。
func TestExternalWinRequiresLocalEvidenceAndRetainsOriginalSnapshot(t *testing.T) {
	store := &winEvidenceMemory{}
	service, repo := localWinService(t, store)
	reference := "外部回调原始引用"
	ctx := createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "machine-a"})
	input := ExternalStatusRequest{OpportunityID: 7, Type: "投标", SourceID: "BID-7", Status: "投标中标", ContractRef: &reference, ChangedAt: service.now()}
	result, err := service.ApplyExternalStatus(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.TerminalPendingType != PendingWinEvidence || result.ContractRef != nil || store.saved != nil {
		t.Fatalf("回调伪造完成依据: %+v", result)
	}
	var snapshot ExternalStatusSnapshot
	if err = json.Unmarshal(repo.externalReplay.SnapshotJSON, &snapshot); err != nil || snapshot.ContractRef == nil || *snapshot.ContractRef != reference {
		t.Fatalf("原始快照被改写: snapshot=%+v err=%v", snapshot, err)
	}
	if _, err = service.ApplyExternalStatus(ctx, input); err != nil || repo.stageUpdates != 1 || repo.externalCreates != 1 {
		t.Fatalf("精确重放失效: %v", err)
	}
}
