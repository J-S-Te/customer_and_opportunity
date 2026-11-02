package contractreference

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestActiveCustomerFollowsCurrentOwnerOnMySQL 在隔离测试库上验证交接后的合同引用权限：
// 创建人与创建历史保持不变，但访问权限必须跟随 owner_user_id。
// 未提供 CRM_INTEGRATION_MYSQL_DSN 时跳过，绝不连接业务库。
func TestActiveCustomerFollowsCurrentOwnerOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-contract-reference-" + time.Now().UTC().Format("20060102150405.000000000")
	now := time.Now().UTC()
	record := &customer.Customer{
		Model:              database.Model{TenantID: tenant, CreatedBy: "old-seller", UpdatedBy: "old-seller", Version: 1, CreatedAt: now, UpdatedAt: now},
		CustomerNo:         "CUS-REF-" + now.Format("150405.000000"),
		Name:               "合同引用范围客户",
		NormalizedName:     "合同引用范围客户",
		CustomerType:       "ENTERPRISE",
		Industry:           "IT",
		Region:             "CN",
		OwnerUserID:        "new-seller",
		OwnerOrgID:         "org-2",
		Status:             customer.StatusActive,
		CreditLevel:        "B",
		CreditChangeSource: "INITIAL",
	}
	if err := db.Create(record).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("tenant_id=? AND id=?", tenant, record.ID).Delete(&customer.Customer{}).Error })

	repository := NewGORMRepository(db)
	ctx := context.Background()
	formerCreator := auth.Principal{TenantID: tenant, UserID: "old-seller", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
	currentOwner := auth.Principal{TenantID: tenant, UserID: "new-seller", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
	administrator := auth.Principal{TenantID: tenant, UserID: "admin", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll}
	crossTenant := auth.Principal{TenantID: tenant + "-other", UserID: "new-seller", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll}

	if _, err := repository.ActiveCustomer(ctx, formerCreator, record.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("交接后原创建人 sales 必须被拒: err=%v", err)
	}
	reference, err := repository.ActiveCustomer(ctx, currentOwner, record.ID)
	if err != nil {
		t.Fatalf("现负责人 sales 应当放行: %v", err)
	}
	if reference.ID != record.ID || reference.Name != record.Name || reference.Status != customer.StatusActive {
		t.Fatalf("引用内容不符合权威客户快照: %+v", reference)
	}
	if _, err := repository.ActiveCustomer(ctx, administrator, record.ID); err != nil {
		t.Fatalf("租户管理员范围不变，应可引用: %v", err)
	}
	if _, err := repository.ActiveCustomer(ctx, crossTenant, record.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("跨租户必须被拒: err=%v", err)
	}

	// 交接回原创建人后权限立即跟随，验证不是一次性缓存或 created_by 兜底。
	if err := db.Model(&customer.Customer{}).Where("tenant_id=? AND id=?", tenant, record.ID).
		Updates(map[string]any{"owner_user_id": "old-seller", "version": gorm.Expr("version+1")}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActiveCustomer(ctx, currentOwner, record.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("交接回退后原负责人必须失去引用权限: err=%v", err)
	}
	if _, err := repository.ActiveCustomer(ctx, formerCreator, record.ID); err != nil {
		t.Fatalf("重新成为负责人的创建人应当放行: %v", err)
	}
}
