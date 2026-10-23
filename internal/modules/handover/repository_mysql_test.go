package handover

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 测试用的最小表模型：只声明仓储真正查询/更新的列。
type testCustomerRow struct {
	UpdatedBy   string         `gorm:"column:updated_by"`
	ID          uint64         `gorm:"primaryKey"`
	TenantID    string         `gorm:"column:tenant_id"`
	Name        string         `gorm:"column:name"`
	OwnerUserID string         `gorm:"column:owner_user_id"`
	OwnerOrgID  string         `gorm:"column:owner_org_id"`
	Status      string         `gorm:"column:status"`
	Version     uint64         `gorm:"column:version"`
	UpdatedAt   time.Time      `gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (testCustomerRow) TableName() string { return "crm_customers" }

type testOpportunityRow struct {
	UpdatedBy   string         `gorm:"column:updated_by"`
	ID          uint64         `gorm:"primaryKey"`
	TenantID    string         `gorm:"column:tenant_id"`
	Name        string         `gorm:"column:name"`
	OwnerUserID string         `gorm:"column:owner_user_id"`
	OwnerOrgID  string         `gorm:"column:owner_org_id"`
	OppStatus   string         `gorm:"column:opp_status"`
	Version     uint64         `gorm:"column:version"`
	UpdatedAt   time.Time      `gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (testOpportunityRow) TableName() string { return "crm_opportunities" }

// handoverTestRepository 在显式提供的一次性 MySQL schema 上建表；
// 未配置 HANDOVER_TEST_MYSQL_DSN 时跳过，不用模拟替代真实 MySQL 行为。
func handoverTestRepository(t *testing.T) (*GORMRepository, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("HANDOVER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("HANDOVER_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open disposable MySQL schema: %v", err)
	}
	models := []any{&freezeRow{}, &receiptRow{}, &testCustomerRow{}, &testOpportunityRow{}, &testMemberRow{}, &testPresaleRequestRow{}, &testPresaleAssignmentRow{}}
	for _, model := range models {
		_ = db.Migrator().DropTable(model)
	}
	t.Cleanup(func() {
		for _, model := range models {
			_ = db.Migrator().DropTable(model)
		}
	})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}
	repository, err := NewGORMRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	return repository, db
}

func seedHandoverCustomer(t *testing.T, db *gorm.DB, tenantID, ownerID, status string, version uint64) uint64 {
	t.Helper()
	row := testCustomerRow{TenantID: tenantID, Name: "客户-" + status, OwnerUserID: ownerID, OwnerOrgID: "org-old", Status: status, Version: version, UpdatedAt: time.Now().UTC()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return row.ID
}

func seedHandoverOpportunity(t *testing.T, db *gorm.DB, tenantID, ownerID, status string, version uint64) uint64 {
	t.Helper()
	row := testOpportunityRow{TenantID: tenantID, Name: "商机-" + status, OwnerUserID: ownerID, OwnerOrgID: "org-old", OppStatus: status, Version: version, UpdatedAt: time.Now().UTC()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	return row.ID
}

func TestFreezeAndResponsibilitiesOnMySQL(t *testing.T) {
	repository, db := handoverTestRepository(t)
	ctx := context.Background()
	tenant, leaver := "tenant-crm-1", "user-leaver"
	activeCustomer := seedHandoverCustomer(t, db, tenant, leaver, "ACTIVE", 3)
	seedHandoverCustomer(t, db, tenant, leaver, "VOID", 1)
	openOpportunity := seedHandoverOpportunity(t, db, tenant, leaver, "FOLLOWING", 2)
	seedHandoverOpportunity(t, db, tenant, leaver, "CLOSED", 1)
	seedHandoverCustomer(t, db, tenant, "user-other", "ACTIVE", 1)
	seedHandoverCustomer(t, db, "tenant-other", leaver, "ACTIVE", 1)

	now := time.Now().UTC()
	freeze, created, err := repository.CreateFreeze(ctx, Freeze{ID: "01CRMFREEZE00000000000001", TenantID: tenant, BatchID: "01CRMBATCH000000000000001", RequestID: "01CRMREQUEST0000000000001", UserID: leaver, Status: FreezeFrozen, FrozenAt: now})
	if err != nil || !created {
		t.Fatalf("freeze = %+v created=%v err=%v", freeze, created, err)
	}
	again, created, err := repository.CreateFreeze(ctx, Freeze{ID: "01CRMFREEZE00000000000002", TenantID: tenant, BatchID: freeze.BatchID, RequestID: freeze.RequestID, UserID: leaver, Status: FreezeFrozen, FrozenAt: now})
	if err != nil || created || again.ID != freeze.ID {
		t.Fatalf("repeated freeze must be idempotent: %+v created=%v err=%v", again, created, err)
	}
	if active, found, err := repository.ActiveFreeze(ctx, tenant, leaver); err != nil || !found || active.BatchID != freeze.BatchID {
		t.Fatalf("active freeze = %+v found=%v err=%v", active, found, err)
	}

	items, err := repository.ListResponsibilities(ctx, tenant, leaver, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("responsibilities = %d (%+v), want only ACTIVE customer + FOLLOWING opportunity", len(items), items)
	}
	seen := map[string]Responsibility{}
	for _, item := range items {
		seen[item.ResourceID] = item
	}
	customerKey := "customer:" + itoa(activeCustomer)
	if item, ok := seen[customerKey]; !ok || item.Category != CategoryCustomerOwner || item.Version != 3 {
		t.Fatalf("customer responsibility wrong: %+v", seen)
	}
	opportunityKey := "opportunity:" + itoa(openOpportunity)
	if item, ok := seen[opportunityKey]; !ok || item.Category != CategoryOpportunityOwner || item.Version != 2 {
		t.Fatalf("opportunity responsibility wrong: %+v", seen)
	}

	if err := repository.ReleaseFreeze(ctx, tenant, freeze.BatchID, "COMPLETED", now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.ActiveFreeze(ctx, tenant, leaver); err != nil || found {
		t.Fatalf("freeze must be released: found=%v err=%v", found, err)
	}
	if err := repository.ReleaseFreeze(ctx, tenant, freeze.BatchID, "COMPLETED", now); err != nil {
		t.Fatalf("repeated release must be idempotent: %v", err)
	}
	if err := repository.ReleaseFreeze(ctx, tenant, "01CRMBATCH000000000000999", "COMPLETED", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing batch release err = %v, want ErrNotFound", err)
	}
}

func TestTransferOwnershipOnMySQL(t *testing.T) {
	repository, db := handoverTestRepository(t)
	ctx := context.Background()
	tenant, leaver, successor := "tenant-crm-2", "user-leaver", "user-successor"
	customerID := seedHandoverCustomer(t, db, tenant, leaver, "ACTIVE", 3)
	opportunityID := seedHandoverOpportunity(t, db, tenant, leaver, "FOLLOWING", 5)
	now := time.Now().UTC()

	customerInput := TransferInput{TenantID: tenant, BatchID: "01CRMBATCH000000000000010", ItemID: "01CRMITEM0000000000000001",
		ResourceType: ResourceCustomer, ResourceID: "customer:" + itoa(customerID), FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: 3, IdempotencyKey: "handover:batch:item-1"}
	receipt, err := repository.TransferOwnership(ctx, customerInput, now)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != ReceiptApplied || receipt.AppliedVersion != 4 || receipt.EvidenceRef == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	var customer testCustomerRow
	if err := db.First(&customer, customerID).Error; err != nil {
		t.Fatal(err)
	}
	if customer.OwnerUserID != successor || customer.Version != 4 {
		t.Fatalf("customer owner=%s version=%d", customer.OwnerUserID, customer.Version)
	}

	replayed, err := repository.TransferOwnership(ctx, customerInput, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != ReceiptApplied || replayed.AppliedVersion != 4 {
		t.Fatalf("replayed receipt = %+v", replayed)
	}
	if err := db.First(&customer, customerID).Error; err != nil {
		t.Fatal(err)
	}
	if customer.Version != 4 {
		t.Fatalf("replay must not change the customer, version = %d", customer.Version)
	}

	stale := customerInput
	stale.IdempotencyKey = "handover:batch:item-stale"
	stale.ExpectedVersion = 2
	rejected, err := repository.TransferOwnership(ctx, stale, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != ReceiptRejected || rejected.ReasonCode != versionConflictReason {
		t.Fatalf("stale receipt = %+v", rejected)
	}
	if _, found, err := repository.FindReceipt(ctx, tenant, stale.IdempotencyKey); err != nil || !found {
		t.Fatalf("rejected receipt must be persisted: found=%v err=%v", found, err)
	}
	if _, found, err := repository.FindReceipt(ctx, tenant, "missing-key"); err != nil || found {
		t.Fatalf("unknown key must report not found: found=%v err=%v", found, err)
	}

	opportunityInput := TransferInput{TenantID: tenant, BatchID: "01CRMBATCH000000000000011", ItemID: "01CRMITEM0000000000000002",
		ResourceType: ResourceOpportunity, ResourceID: "opportunity:" + itoa(opportunityID), FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: 5, IdempotencyKey: "handover:batch:item-2"}
	if receipt, err := repository.TransferOwnership(ctx, opportunityInput, now); err != nil || receipt.Status != ReceiptApplied || receipt.AppliedVersion != 6 {
		t.Fatalf("opportunity transfer = %+v err=%v", receipt, err)
	}
	var opportunity testOpportunityRow
	if err := db.First(&opportunity, opportunityID).Error; err != nil {
		t.Fatal(err)
	}
	if opportunity.OwnerUserID != successor || opportunity.Version != 6 {
		t.Fatalf("opportunity owner=%s version=%d", opportunity.OwnerUserID, opportunity.Version)
	}

	if _, err := repository.TransferOwnership(ctx, TransferInput{TenantID: tenant, BatchID: "b", ItemID: "i",
		ResourceType: "unknown", ResourceID: "x:1", FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: 1, IdempotencyKey: "handover:batch:item-3"}, now); !errors.Is(err, ErrValidation) {
		t.Fatalf("unsupported resource type must be rejected: %v", err)
	}
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// 活动团队成员与未完成售前任务同样是权威责任。
type testMemberRow struct {
	EndedAt       *time.Time     `gorm:"column:ended_at"`
	Version       uint64         `gorm:"column:version"`
	CreatedBy     string         `gorm:"column:created_by;size:64"`
	UpdatedBy     string         `gorm:"column:updated_by;size:64"`
	CreatedAt     time.Time      `gorm:"column:created_at"`
	ID            uint64         `gorm:"primaryKey"`
	TenantID      string         `gorm:"column:tenant_id;size:64"`
	OpportunityID uint64         `gorm:"column:opportunity_id"`
	UserID        string         `gorm:"column:user_id;size:64"`
	Role          string         `gorm:"column:role;size:32"`
	IsActive      bool           `gorm:"column:is_active"`
	UpdatedAt     time.Time      `gorm:"column:updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (testMemberRow) TableName() string { return "crm_opportunity_members" }

type testPresaleRequestRow struct {
	ID        uint64         `gorm:"primaryKey"`
	TenantID  string         `gorm:"column:tenant_id;size:64"`
	Title     string         `gorm:"column:title"`
	Status    string         `gorm:"column:status;size:32"`
	Version   uint64         `gorm:"column:version"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (testPresaleRequestRow) TableName() string { return "crm_presale_requests" }

type testPresaleAssignmentRow struct {
	AssigneeNameSnapshot       string         `gorm:"column:assignee_name_snapshot;size:128"`
	AssigneeDepartmentSnapshot string         `gorm:"column:assignee_department_snapshot;size:128"`
	AssigneeRole               string         `gorm:"column:assignee_role;size:32"`
	AssignmentKind             string         `gorm:"column:assignment_kind;size:16;default:'EXECUTOR'"`
	AssignedBy                 string         `gorm:"column:assigned_by;size:64"`
	AssignedAt                 *time.Time     `gorm:"column:assigned_at"`
	BatchNo                    uint64         `gorm:"column:batch_no"`
	ChangeReason               string         `gorm:"column:change_reason;size:1000"`
	Version                    uint64         `gorm:"column:version"`
	CreatedBy                  string         `gorm:"column:created_by;size:64"`
	UpdatedBy                  string         `gorm:"column:updated_by;size:64"`
	CreatedAt                  time.Time      `gorm:"column:created_at"`
	ID                         uint64         `gorm:"primaryKey"`
	TenantID                   string         `gorm:"column:tenant_id;size:64"`
	RequestID                  uint64         `gorm:"column:request_id"`
	AssigneeID                 string         `gorm:"column:assignee_id;size:64"`
	IsCurrent                  bool           `gorm:"column:is_current"`
	EndedAt                    *time.Time     `gorm:"column:ended_at"`
	UpdatedAt                  time.Time      `gorm:"column:updated_at"`
	DeletedAt                  gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (testPresaleAssignmentRow) TableName() string { return "crm_presale_assignments" }

func TestListResponsibilitiesIncludesTeamAndPresaleOnMySQL(t *testing.T) {
	repository, db := handoverTestRepository(t)
	ctx := context.Background()
	tenant, leaver, other := "tenant-crm-3", "user-leaver", "user-other"
	now := time.Now().UTC()

	openOpportunity := seedHandoverOpportunity(t, db, tenant, other, "FOLLOWING", 4)
	closedOpportunity := seedHandoverOpportunity(t, db, tenant, other, "CLOSED", 2)
	for _, member := range []testMemberRow{
		{TenantID: tenant, OpportunityID: openOpportunity, UserID: leaver, Role: "SALES_SUPPORT", IsActive: true, UpdatedAt: now},
		{TenantID: tenant, OpportunityID: closedOpportunity, UserID: leaver, Role: "SALES_SUPPORT", IsActive: true, UpdatedAt: now},
		{TenantID: tenant, OpportunityID: openOpportunity, UserID: other, Role: "TECHNICAL_SUPPORT", IsActive: true, UpdatedAt: now},
		{TenantID: tenant, OpportunityID: openOpportunity, UserID: "user-ended", Role: "OTHER", IsActive: false, UpdatedAt: now},
	} {
		if err := db.Create(&member).Error; err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}

	runningRequest := seedPresaleRequest(t, db, tenant, "执行中的售前", "EXECUTING", 7)
	completedRequest := seedPresaleRequest(t, db, tenant, "已完成的售前", "COMPLETED", 3)
	assignments := []testPresaleAssignmentRow{
		{TenantID: tenant, RequestID: runningRequest, AssigneeID: leaver, IsCurrent: true, AssignmentKind: "EXECUTOR", UpdatedAt: now},
		{TenantID: tenant, RequestID: completedRequest, AssigneeID: leaver, IsCurrent: true, AssignmentKind: "EXECUTOR", UpdatedAt: now},
		// TS-011 团队负责人不是执行责任；不能出现在按执行人展开的交接清单中。
		{TenantID: tenant, RequestID: runningRequest, AssigneeID: leaver, AssigneeRole: "team_lead", IsCurrent: true, AssignmentKind: "TEAM_LEAD", UpdatedAt: now},
	}
	for _, assignment := range assignments {
		if err := db.Create(&assignment).Error; err != nil {
			t.Fatalf("seed assignment: %v", err)
		}
	}
	ended := now.Add(-time.Hour)
	if err := db.Create(&testPresaleAssignmentRow{TenantID: tenant, RequestID: runningRequest, AssigneeID: "user-ended", IsCurrent: false, EndedAt: &ended, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	items, err := repository.ListResponsibilities(ctx, tenant, leaver, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]Responsibility{}
	for _, item := range items {
		seen[item.ResourceID] = item
	}
	teamKey := "opportunity-team:" + itoa(openOpportunity)
	if item, ok := seen[teamKey]; !ok || item.Category != CategoryOpportunityTeam || item.ResourceType != ResourceOpportunityTeam {
		t.Fatalf("team responsibility missing or mislabelled: %v", seen)
	}
	presaleKey := "presale:" + itoa(runningRequest)
	if item, ok := seen[presaleKey]; !ok || item.Category != CategoryPresaleTask || item.ResourceType != ResourcePresaleTask {
		t.Fatalf("presale responsibility missing or mislabelled: %v", seen)
	}
	for _, unexpected := range []string{"opportunity-team:" + itoa(closedOpportunity), "presale:" + itoa(completedRequest)} {
		if _, present := seen[unexpected]; present {
			t.Fatalf("terminal responsibility leaked: %s", unexpected)
		}
	}
	if len(items) != 2 {
		t.Fatalf("items = %d (%v), want exactly the active team membership and running presale task", len(items), seen)
	}
}

func seedPresaleRequest(t *testing.T, db *gorm.DB, tenantID, title, status string, version uint64) uint64 {
	t.Helper()
	row := testPresaleRequestRow{TenantID: tenantID, Title: title, Status: status, Version: version, UpdatedAt: time.Now().UTC()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed presale request: %v", err)
	}
	return row.ID
}

// 团队与售前责任项的转移必须按领域语义追加式生效。
func TestTransferTeamAndPresaleOnMySQL(t *testing.T) {
	repository, db := handoverTestRepository(t)
	ctx := context.Background()
	tenant, leaver, successor := "tenant-crm-4", "user-leaver", "user-successor"
	now := time.Now().UTC()
	opportunityID := seedHandoverOpportunity(t, db, tenant, "user-owner", "FOLLOWING", 4)

	member := testMemberRow{TenantID: tenant, OpportunityID: opportunityID, UserID: leaver, Role: "SALES_SUPPORT", IsActive: true, Version: 1, UpdatedAt: now}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	teamReceipt, err := repository.TransferOwnership(ctx, TransferInput{
		TenantID: tenant, BatchID: "batch-team", ItemID: "item-team",
		ResourceType: ResourceOpportunityTeam, ResourceID: "opportunity-team:" + itoa(opportunityID),
		FromUserID: leaver, ToUserID: successor, ExpectedVersion: 1, IdempotencyKey: "handover:batch-team:item-team",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if teamReceipt.Status != ReceiptApplied || teamReceipt.AppliedVersion != 2 || teamReceipt.EvidenceRef == "" {
		t.Fatalf("team receipt = %+v", teamReceipt)
	}
	var closedMember testMemberRow
	if err := db.Where("opportunity_id = ? AND user_id = ?", opportunityID, leaver).Take(&closedMember).Error; err != nil {
		t.Fatal(err)
	}
	if closedMember.IsActive || closedMember.EndedAt == nil || closedMember.Version != 2 {
		t.Fatalf("prior membership must be ended: %+v", closedMember)
	}
	var newMember testMemberRow
	if err := db.Where("opportunity_id = ? AND user_id = ?", opportunityID, successor).Take(&newMember).Error; err != nil {
		t.Fatalf("successor membership missing: %v", err)
	}
	if !newMember.IsActive || newMember.Role != "SALES_SUPPORT" {
		t.Fatalf("successor membership wrong: %+v", newMember)
	}
	if replayed, err := repository.TransferOwnership(ctx, TransferInput{
		TenantID: tenant, BatchID: "batch-team", ItemID: "item-team",
		ResourceType: ResourceOpportunityTeam, ResourceID: "opportunity-team:" + itoa(opportunityID),
		FromUserID: leaver, ToUserID: successor, ExpectedVersion: 1, IdempotencyKey: "handover:batch-team:item-team",
	}, now.Add(time.Minute)); err != nil || replayed.Status != ReceiptApplied {
		t.Fatalf("team replay = %+v err=%v", replayed, err)
	}
	// 承接人已是团队成员时不得重复插入，回执为 REJECTED 且原因明确。
	if err := db.Create(&testMemberRow{TenantID: tenant, OpportunityID: opportunityID, UserID: "user-owner", Role: "OTHER", IsActive: true, Version: 1, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	existsReceipt, err := repository.TransferOwnership(ctx, TransferInput{
		TenantID: tenant, BatchID: "batch-team", ItemID: "item-team2",
		ResourceType: ResourceOpportunityTeam, ResourceID: "opportunity-team:" + itoa(opportunityID),
		FromUserID: leaver, ToUserID: "user-owner", ExpectedVersion: 1, IdempotencyKey: "handover:batch-team:item-team2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if existsReceipt.Status != ReceiptRejected || existsReceipt.ReasonCode != "CRM_TEAM_MEMBER_EXISTS" {
		t.Fatalf("existing member receipt = %+v", existsReceipt)
	}

	requestID := seedPresaleRequest(t, db, tenant, "执行中的售前", "EXECUTING", 3)
	assignment := testPresaleAssignmentRow{TenantID: tenant, RequestID: requestID, AssigneeID: leaver, AssigneeRole: "PRESALE_ENGINEER",
		AssigneeNameSnapshot: "离职工程师", AssigneeDepartmentSnapshot: "售前部", AssignedBy: "user-owner", BatchNo: 1,
		ChangeReason: "首次指派", IsCurrent: true, Version: 1, UpdatedAt: now}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	presaleReceipt, err := repository.TransferOwnership(ctx, TransferInput{
		TenantID: tenant, BatchID: "batch-presale", ItemID: "item-presale",
		ResourceType: ResourcePresaleTask, ResourceID: "presale:" + itoa(requestID),
		FromUserID: leaver, ToUserID: successor, ExpectedVersion: 1, IdempotencyKey: "handover:batch-presale:item-presale",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if presaleReceipt.Status != ReceiptApplied || presaleReceipt.AppliedVersion != 2 {
		t.Fatalf("presale receipt = %+v", presaleReceipt)
	}
	var priorAssignment testPresaleAssignmentRow
	if err := db.Where("request_id = ? AND assignee_id = ?", requestID, leaver).Take(&priorAssignment).Error; err != nil {
		t.Fatal(err)
	}
	if priorAssignment.IsCurrent || priorAssignment.EndedAt == nil {
		t.Fatalf("prior assignment must be ended: %+v", priorAssignment)
	}
	var currentAssignment testPresaleAssignmentRow
	if err := db.Where("request_id = ? AND is_current = ?", requestID, true).Take(&currentAssignment).Error; err != nil {
		t.Fatalf("successor assignment missing: %v", err)
	}
	if currentAssignment.AssigneeID != successor || currentAssignment.BatchNo != 2 || currentAssignment.AssigneeRole != "PRESALE_ENGINEER" {
		t.Fatalf("successor assignment wrong: %+v", currentAssignment)
	}

	// 团队负责人不是可自动转移的执行责任：交接必须拒绝，并要求技术总监在售前申请上重选。
	leadRequestID := seedPresaleRequest(t, db, tenant, "等待团队负责人指派", "PENDING_EXECUTION_ASSIGNMENT", 2)
	leadAssignment := testPresaleAssignmentRow{TenantID: tenant, RequestID: leadRequestID, AssigneeID: leaver, AssigneeRole: "team_lead",
		AssigneeNameSnapshot: "离职负责人", AssigneeDepartmentSnapshot: "交付一部", AssignedBy: "user-owner", BatchNo: 1,
		ChangeReason: "团队负责人指派", IsCurrent: true, AssignmentKind: "TEAM_LEAD", Version: 1, UpdatedAt: now}
	if err := db.Create(&leadAssignment).Error; err != nil {
		t.Fatal(err)
	}
	leadReceipt, err := repository.TransferOwnership(ctx, TransferInput{
		TenantID: tenant, BatchID: "batch-presale-lead", ItemID: "item-presale-lead",
		ResourceType: ResourcePresaleTask, ResourceID: "presale:" + itoa(leadRequestID),
		FromUserID: leaver, ToUserID: successor, ExpectedVersion: 1, IdempotencyKey: "handover:batch-presale-lead:item-presale-lead",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if leadReceipt.Status != ReceiptRejected || leadReceipt.ReasonCode != presaleTeamLeadReselectionReason {
		t.Fatalf("team lead transfer receipt = %+v", leadReceipt)
	}
	var unchangedLead testPresaleAssignmentRow
	if err := db.Where("request_id = ? AND assignee_id = ?", leadRequestID, leaver).Take(&unchangedLead).Error; err != nil {
		t.Fatal(err)
	}
	if !unchangedLead.IsCurrent || unchangedLead.EndedAt != nil {
		t.Fatalf("team lead assignment must remain current: %+v", unchangedLead)
	}
}

func TestReceiptIDsDoNotCollideForDifferentIdempotencyKeys(t *testing.T) {
	repository, db := handoverTestRepository(t)
	ctx := context.Background()
	tenant, leaver, successor := "tenant-crm-receipt-id", "user-leaver", "user-successor"
	now := time.Now().UTC()
	for index, key := range []string{"handover:Aa", "handover:BB"} {
		customerID := seedHandoverCustomer(t, db, tenant, leaver, "ACTIVE", 1)
		input := TransferInput{TenantID: tenant, BatchID: "01CRMBATCH000000000000020", ItemID: "01CRMITEM000000000000001" + string(rune('0'+index)),
			ResourceType: ResourceCustomer, ResourceID: "customer:" + itoa(customerID), FromUserID: leaver, ToUserID: successor,
			ExpectedVersion: 1, IdempotencyKey: key}
		receipt, err := repository.TransferOwnership(ctx, input, now)
		if err != nil {
			t.Fatalf("transfer %s: %v", key, err)
		}
		if receipt.Status != ReceiptApplied || receipt.AppliedVersion != 2 {
			t.Fatalf("transfer %s receipt=%+v", key, receipt)
		}
	}
	var count int64
	if err := db.Model(&receiptRow{}).Where("tenant_id=? AND idempotency_key IN ?", tenant, []string{"handover:Aa", "handover:BB"}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("receipt count=%d, want two distinct receipts", count)
	}
	var distinct int64
	if err := db.Model(&receiptRow{}).Where("tenant_id=? AND idempotency_key IN ?", tenant, []string{"handover:Aa", "handover:BB"}).Distinct("id").Count(&distinct).Error; err != nil {
		t.Fatal(err)
	}
	if distinct != 2 {
		t.Fatalf("distinct receipt ids=%d, want 2", distinct)
	}
}

type ownerOrgStub struct{ org string }

func (stub ownerOrgStub) PrimaryOrganization(context.Context, string, string) (string, error) {
	return stub.org, nil
}

func TestTransferUpdatesOwnerOrganizationOnMySQL(t *testing.T) {
	repository, db := handoverTestRepository(t)
	repository.UseOwnerOrgResolver(ownerOrgStub{org: "org-new"})
	ctx := context.Background()
	tenant, leaver, successor := "tenant-crm-org", "user-leaver", "user-successor"
	customerID := seedHandoverCustomer(t, db, tenant, leaver, "ACTIVE", 1)
	opportunityID := seedHandoverOpportunity(t, db, tenant, leaver, "FOLLOWING", 1)
	now := time.Now().UTC()
	if receipt, err := repository.TransferOwnership(ctx, TransferInput{TenantID: tenant, BatchID: "01CRMBATCH000000000000030", ItemID: "01CRMITEM0000000000000031",
		ResourceType: ResourceCustomer, ResourceID: "customer:" + itoa(customerID), FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: 1, IdempotencyKey: "handover:org:customer"}, now); err != nil || receipt.Status != ReceiptApplied {
		t.Fatalf("customer transfer receipt=%+v err=%v", receipt, err)
	}
	if receipt, err := repository.TransferOwnership(ctx, TransferInput{TenantID: tenant, BatchID: "01CRMBATCH000000000000030", ItemID: "01CRMITEM0000000000000032",
		ResourceType: ResourceOpportunity, ResourceID: "opportunity:" + itoa(opportunityID), FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: 1, IdempotencyKey: "handover:org:opportunity"}, now); err != nil || receipt.Status != ReceiptApplied {
		t.Fatalf("opportunity transfer receipt=%+v err=%v", receipt, err)
	}
	var customer testCustomerRow
	if err := db.First(&customer, customerID).Error; err != nil {
		t.Fatal(err)
	}
	if customer.OwnerUserID != successor || customer.OwnerOrgID != "org-new" {
		t.Fatalf("customer owner=%s org=%s", customer.OwnerUserID, customer.OwnerOrgID)
	}
	var opportunity testOpportunityRow
	if err := db.First(&opportunity, opportunityID).Error; err != nil {
		t.Fatal(err)
	}
	if opportunity.OwnerUserID != successor || opportunity.OwnerOrgID != "org-new" {
		t.Fatalf("opportunity owner=%s org=%s", opportunity.OwnerUserID, opportunity.OwnerOrgID)
	}
}
