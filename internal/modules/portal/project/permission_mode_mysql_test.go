package project

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestIsolatedAccountNeverRegainsLegacyCustomerScopeOnMySQL 验证门户
// 零授权反例：移除最后一个绑定后，已标记
// ISOLATED 的账号不得看到任何项目；未迁移账号仍保持
// 旧版客户全范围行为。
func TestIsolatedAccountNeverRegainsLegacyCustomerScopeOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTAL_PROJECT_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("PORTAL_PROJECT_TEST_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-portal-mode-" + time.Now().UTC().Format("20060102150405.000000000")
	const customerID uint64 = 991188
	now := time.Now().UTC()
	makeSnapshot := func(projectID string) *Snapshot {
		return &Snapshot{Model: database.Model{TenantID: tenant, CreatedBy: "qa", UpdatedBy: "qa", CreatedAt: now, UpdatedAt: now, Version: 1}, ProjectID: projectID, CustomerID: customerID, ProjectName: projectID, Status: "IN_PROGRESS", ProgressPct: 10, CurrentStage: "实施", SourceUpdatedAt: now, SyncedAt: now, RawVersion: "v1"}
	}
	first, second := makeSnapshot("PROJECT-A"), makeSnapshot("PROJECT-B")
	if err := db.Create(first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(second).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Where("tenant_id=? AND customer_id=?", tenant, customerID).Delete(&AccountBinding{}).Error
		_ = db.Where("tenant_id=? AND customer_id=?", tenant, customerID).Delete(&AccountPermissionMode{}).Error
		_ = db.Where("tenant_id=? AND customer_id=?", tenant, customerID).Delete(&Snapshot{}).Error
	})
	repo := NewGORMRepository(db)
	ctx := context.Background()
	legacyScope := Scope{TenantID: tenant, CustomerID: customerID, AccountID: "legacy-account"}
	legacy, err := repo.List(ctx, legacyScope, ListQuery{Page: 1, PageSize: 20})
	if err != nil || legacy.Total != 2 {
		t.Fatalf("legacy total=%d err=%v, want 2 customer projects", legacy.Total, err)
	}

	if err := repo.SyncAccountBindings(ctx, tenant, customerID, "PROJECT-A", "v1", []string{"isolated-account"}, now); err != nil {
		t.Fatal(err)
	}
	isolatedScope := Scope{TenantID: tenant, CustomerID: customerID, AccountID: "isolated-account"}
	isolated, err := repo.List(ctx, isolatedScope, ListQuery{Page: 1, PageSize: 20})
	if err != nil || isolated.Total != 1 || isolated.Items[0].ProjectID != "PROJECT-A" {
		t.Fatalf("isolated total=%d items=%+v err=%v, want only PROJECT-A", isolated.Total, isolated.Items, err)
	}
	// 撤销最后一项授权不得回退到客户的全部项目。
	if err := repo.SyncAccountBindings(ctx, tenant, customerID, "PROJECT-A", "v2", nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	empty, err := repo.List(ctx, isolatedScope, ListQuery{Page: 1, PageSize: 20})
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("zero-grant total=%d items=%+v err=%v, want empty", empty.Total, empty.Items, err)
	}
	// 没有模式行的其他账号仍使用旧版行为。
	legacyAgain, err := repo.List(ctx, legacyScope, ListQuery{Page: 1, PageSize: 20})
	if err != nil || legacyAgain.Total != 2 {
		t.Fatalf("legacy after another account isolation total=%d err=%v, want 2", legacyAgain.Total, err)
	}
}
