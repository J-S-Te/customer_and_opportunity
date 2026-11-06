package portalbootstrap

import (
	"context"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func portalDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 与其它子模块一致：不连业务库，只用 GORM DryRun 断言 SQL 形状。
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	return db
}

func TestCRMCustomerDirectoryQueryBindsTenantAndExcludesUnusableCustomers(t *testing.T) {
	var count int64
	statement := crmCustomerExistsQuery(portalDryRunDB(t), "tenant-a", 7).Count(&count).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"crm_customers", "tenant_id = ?", "id = ?", "status = 'ACTIVE'", "merged_into_id IS NULL", "deleted_at IS NULL"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing %q in %q", fragment, sql)
		}
	}
	if len(statement.Vars) < 2 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != uint64(7) {
		t.Fatalf("vars=%#v", statement.Vars)
	}
}

func TestCRMCustomerDirectoryFailsClosedWhenUnconfigured(t *testing.T) {
	directory := newCRMCustomerDirectory(nil)
	exists, err := directory.Exists(context.Background(), "tenant-a", 7)
	if err == nil || exists {
		t.Fatalf("nil database must fail closed: exists=%v err=%v", exists, err)
	}
	var zero *crmCustomerDirectory
	if exists, err = zero.Exists(context.Background(), "tenant-a", 7); err == nil || exists {
		t.Fatalf("nil directory must fail closed: exists=%v err=%v", exists, err)
	}
	// 空租户或零客户号直接判定不存在，不再访问数据库（此处 db 为 nil，访问会失败关闭）。
	exists, err = directory.Exists(context.Background(), "", 7)
	if err != nil || exists {
		t.Fatalf("blank tenant must resolve to not-found: exists=%v err=%v", exists, err)
	}
}
