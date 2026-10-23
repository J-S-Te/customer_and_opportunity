package presaleengineersyncworker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type testOIDCSession struct {
	ID             uint64     `gorm:"primaryKey"`
	TenantID       string     `gorm:"column:tenant_id;size:64;not null"`
	PlatformUserID string     `gorm:"column:platform_user_id;size:128;not null"`
	RolesJSON      string     `gorm:"column:roles_json;type:json;not null"`
	ExpiresAt      time.Time  `gorm:"column:expires_at;not null"`
	RevokedAt      *time.Time `gorm:"column:revoked_at"`
}

func (testOIDCSession) TableName() string { return "crm_oidc_sessions" }

// TestInvalidateTeamLeadsForInactiveEngineersOnMySQL 只在显式提供一次性测试 DSN 时运行。
// 它验证同步停用负责人后，申请回退、负责人指派结束、状态日志和技术总监站内消息同事务生效。
func TestInvalidateTeamLeadsForInactiveEngineersOnMySQL(t *testing.T) {
	dsn := os.Getenv("PMS_ENGINEER_SYNC_TEST_DSN")
	if dsn == "" {
		t.Skip("PMS_ENGINEER_SYNC_TEST_DSN must point to a disposable MySQL database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	models := []any{
		&presale.PresaleRequest{}, &presale.Engineer{}, &presale.Assignment{},
		&presale.AssignmentEvent{}, &presale.StatusLog{}, &notification.Notification{}, &testOIDCSession{},
	}
	if err = db.Migrator().DropTable(models...); err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	tenant := "tenant-ts011-sync"
	request := &presale.PresaleRequest{
		BaseModel: presale.BaseModel{TenantID: tenant, CreatedBy: "test", UpdatedBy: "test", Version: 4},
		RequestNo: "TS-SYNC-1", OpportunityID: 1, OpportunityNoSnapshot: "OPP-1",
		ApplicantID: "sales-1", ApplicantNameSnapshot: "销售", Venue: presale.VenueRemote,
		ContactName: "客户", ContactPhoneCipher: []byte{1}, ContactPhoneMasked: "138****0000",
		Description: "同步停用负责人", ExpectedStart: now, ExpectedEnd: now.Add(time.Hour),
		Urgency: presale.UrgencyNormal, Status: presale.StatusPendingExecutionAssignment,
		ExecutionDepartmentID: "org-1", ExecutionDepartment: "交付一部",
		TeamLeadPersonID: "lead-invalid", TeamLeadNameSnapshot: "失效负责人",
		AssignmentLevel:      presale.AssignmentLevelTeamLead,
		CreateIdempotencyKey: "create-key", CreateRequestHash: "hash",
	}
	if err = db.Create(request).Error; err != nil {
		t.Fatal(err)
	}
	assignment := &presale.Assignment{
		BaseModel: presale.BaseModel{TenantID: tenant, CreatedBy: "test", UpdatedBy: "test", Version: 1},
		RequestID: request.ID, AssigneeID: "lead-invalid", AssigneeNameSnapshot: "失效负责人",
		AssigneeDepartmentSnapshot: "交付一部", AssigneeRole: "team_lead",
		AssignmentKind: presale.AssignmentKindTeamLead, AssignedBy: "technical-director",
		AssignedAt: now, IsCurrent: true, BatchNo: 1, ChangeReason: "团队负责人指派",
	}
	if err = db.Create(assignment).Error; err != nil {
		t.Fatal(err)
	}
	engineer := &presale.Engineer{
		BaseModel: presale.BaseModel{TenantID: tenant, CreatedBy: "pms-sync", UpdatedBy: "pms-sync", Version: 1},
		PersonID:  "lead-invalid", PersonName: "失效负责人", Role: "team_lead", ValidFlag: false,
		SourceUpdatedAt: now, SyncedAt: now,
	}
	if err = db.Create(engineer).Error; err != nil {
		t.Fatal(err)
	}
	session := &testOIDCSession{TenantID: tenant, PlatformUserID: "technical-director", RolesJSON: `["technical_director"]`, ExpiresAt: now.Add(time.Hour)}
	if err = db.Create(session).Error; err != nil {
		t.Fatal(err)
	}
	executingRequest := &presale.PresaleRequest{
		BaseModel: presale.BaseModel{TenantID: tenant, CreatedBy: "test", UpdatedBy: "test", Version: 5},
		RequestNo: "TS-SYNC-EXECUTING", OpportunityID: 1, OpportunityNoSnapshot: "OPP-1",
		ApplicantID: "sales-1", ApplicantNameSnapshot: "销售", Venue: presale.VenueRemote,
		ContactName: "客户", ContactPhoneCipher: []byte{1}, ContactPhoneMasked: "138****0000",
		Description: "执行中负责人失效", ExpectedStart: now, ExpectedEnd: now.Add(time.Hour),
		Urgency: presale.UrgencyNormal, Status: presale.StatusExecuting,
		ExecutionDepartmentID: "org-1", ExecutionDepartment: "交付一部",
		TeamLeadPersonID: "lead-invalid", TeamLeadNameSnapshot: "失效负责人",
		AssignmentLevel:      presale.AssignmentLevelExecutor,
		CreateIdempotencyKey: "create-key-executing", CreateRequestHash: "hash-executing",
	}
	if err = db.Create(executingRequest).Error; err != nil {
		t.Fatal(err)
	}
	executingLeadAssignment := &presale.Assignment{
		BaseModel: presale.BaseModel{TenantID: tenant, CreatedBy: "test", UpdatedBy: "test", Version: 1},
		RequestID: executingRequest.ID, AssigneeID: "lead-invalid", AssigneeNameSnapshot: "失效负责人",
		AssigneeDepartmentSnapshot: "交付一部", AssigneeRole: "team_lead",
		AssignmentKind: presale.AssignmentKindTeamLead, AssignedBy: "technical-director",
		AssignedAt: now, IsCurrent: true, BatchNo: 1, ChangeReason: "团队负责人指派",
	}
	if err = db.Create(executingLeadAssignment).Error; err != nil {
		t.Fatal(err)
	}

	if err = db.Transaction(func(tx *gorm.DB) error {
		return invalidateTeamLeadsForInactiveEngineers(context.Background(), tx, tenant)
	}); err != nil {
		t.Fatal(err)
	}

	var refreshed presale.PresaleRequest
	if err = db.Where("tenant_id=? AND id=?", tenant, request.ID).Take(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != presale.StatusApprovedPendingAssignment || refreshed.TeamLeadPersonID != "" || refreshed.AssignmentLevel != presale.AssignmentLevelNone {
		t.Fatalf("request after invalidation=%+v", refreshed)
	}
	var current int64
	if err = db.Model(&presale.Assignment{}).Where("tenant_id=? AND request_id=? AND assignment_kind=? AND is_current=1", tenant, request.ID, presale.AssignmentKindTeamLead).Count(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current != 0 {
		t.Fatalf("team lead assignment still current=%d", current)
	}
	var log presale.StatusLog
	if err = db.Where("tenant_id=? AND request_id=? AND `trigger`=?", tenant, request.ID, "TEAM_LEAD_INVALID").Take(&log).Error; err != nil {
		t.Fatal(err)
	}
	var message notification.Notification
	if err = db.Where("tenant_id=? AND type=? AND recipient_id=? AND request_id=?", tenant, notification.TypePresaleTeamLeadInvalid, "technical-director", request.ID).Take(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.RequestID != request.ID || message.RequestNo != request.RequestNo {
		t.Fatalf("notification=%+v", message)
	}

	var executingRefreshed presale.PresaleRequest
	if err = db.Where("tenant_id=? AND id=?", tenant, executingRequest.ID).Take(&executingRefreshed).Error; err != nil {
		t.Fatal(err)
	}
	if executingRefreshed.Status != presale.StatusExecuting || executingRefreshed.TeamLeadPersonID != "lead-invalid" || executingRefreshed.Version != 5 {
		t.Fatalf("executing request must remain unchanged: %+v", executingRefreshed)
	}
	var executingLeadCurrent int64
	if err = db.Model(&presale.Assignment{}).Where("tenant_id=? AND request_id=? AND assignment_kind=? AND is_current=1", tenant, executingRequest.ID, presale.AssignmentKindTeamLead).Count(&executingLeadCurrent).Error; err != nil {
		t.Fatal(err)
	}
	if executingLeadCurrent != 1 {
		t.Fatalf("executing team lead assignment must remain current=%d", executingLeadCurrent)
	}
	var executingLogs int64
	if err = db.Model(&presale.StatusLog{}).Where("tenant_id=? AND request_id=?", tenant, executingRequest.ID).Count(&executingLogs).Error; err != nil {
		t.Fatal(err)
	}
	if executingLogs != 0 {
		t.Fatalf("executing invalidation must not write a status transition: %d", executingLogs)
	}
	var executingMessage notification.Notification
	if err = db.Where("tenant_id=? AND type=? AND recipient_id=? AND request_id=?", tenant, notification.TypePresaleTeamLeadInvalid, "technical-director", executingRequest.ID).Take(&executingMessage).Error; err != nil {
		t.Fatal(err)
	}
	if executingMessage.RequestID != executingRequest.ID {
		t.Fatalf("executing notification=%+v", executingMessage)
	}
}
