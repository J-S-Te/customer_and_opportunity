package account

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 新建门户账号没有项目授权，但仍须立即进入
// 项目隔离；否则旧版回退会在首次授权创建前
// 暴露客户的全部项目。
func TestProvisionedPortalAccountStartsIsolatedOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTAL_ACCOUNT_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("PORTAL_ACCOUNT_TEST_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-portal-provision-" + time.Now().UTC().Format("20060102150405.000000000")
	accountID := "new-portal-subject"
	const customerID uint64 = 778899
	now := time.Now().UTC()
	link := &IdentityLink{Model: database.Model{TenantID: tenant, CreatedBy: "qa", UpdatedBy: "qa", CreatedAt: now, UpdatedAt: now, Version: 1}, AccountNo: "QA-PORTAL-NEW", PlatformUserID: accountID, CustomerID: customerID, Status: IdentityPending}
	if _, err := NewGORMRepository(db).UpsertPendingLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		snap := &project.Snapshot{Model: database.Model{TenantID: tenant, CreatedBy: "qa", UpdatedBy: "qa", CreatedAt: now, UpdatedAt: now, Version: 1}, ProjectID: "NEW-PROJECT-" + string(rune('A'+i)), CustomerID: customerID, ProjectName: "new project", Status: "IN_PROGRESS", ProgressPct: 0, CurrentStage: "实施", SourceUpdatedAt: now, SyncedAt: now, RawVersion: "v1"}
		if err := db.Create(snap).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM portal_account_project_permission_modes WHERE tenant_id=? AND customer_id=? AND account_id=?`, tenant, customerID, accountID).Error
		_ = db.Where("tenant_id=? AND platform_user_id=?", tenant, accountID).Delete(&IdentityLink{}).Error
		_ = db.Where("tenant_id=? AND customer_id=?", tenant, customerID).Delete(&project.Snapshot{}).Error
	})
	var mode string
	if err := db.Raw(`SELECT mode FROM portal_account_project_permission_modes WHERE tenant_id=? AND customer_id=? AND account_id=?`, tenant, customerID, accountID).Scan(&mode).Error; err != nil {
		t.Fatal(err)
	}
	if mode != "ISOLATED" {
		t.Fatalf("new account mode=%q, want ISOLATED", mode)
	}
	page, err := project.NewGORMRepository(db).List(context.Background(), project.Scope{TenantID: tenant, CustomerID: customerID, AccountID: accountID}, project.ListQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 0 {
		t.Fatalf("new account visible projects total=%d err=%v, want empty", page.Total, err)
	}
}
