package customer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestSalesCustomerVisibilityFollowsCurrentOwnerOnMySQL 验证交接
// 反例：created_by 仍为原销售人员，但负责人变更后访问权限
// 必须立即跟随 owner_user_id。
func TestSalesCustomerVisibilityFollowsCurrentOwnerOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-owner-scope-" + time.Now().UTC().Format("20060102150405.000000000")
	now := time.Now().UTC()
	customer := &Customer{Model: database.Model{TenantID: tenant, CreatedBy: "old-seller", UpdatedBy: "old-seller", Version: 1, CreatedAt: now, UpdatedAt: now}, CustomerNo: "CUS-OWNER-" + now.Format("150405.000000"), Name: "负责人范围客户", NormalizedName: "负责人范围客户", CustomerType: "ENTERPRISE", Industry: "IT", Region: "CN", OwnerUserID: "old-seller", OwnerOrgID: "org-1", Status: StatusActive, CreditLevel: "B", CreditChangeSource: "INITIAL"}
	if err = db.Create(customer).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("tenant_id=? AND id=?", tenant, customer.ID).Delete(&Customer{}).Error })
	repo := NewGORMRepository(db)
	oldPrincipal := auth.Principal{TenantID: tenant, UserID: "old-seller", Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf}
	newPrincipal := auth.Principal{TenantID: tenant, UserID: "new-seller", Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf}
	ctx := context.Background()
	if _, err := repo.FindByID(ctx, oldPrincipal, customer.ID, false); err != nil {
		t.Fatalf("old owner before transfer: %v", err)
	}
	if _, err := repo.FindByID(ctx, newPrincipal, customer.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("new owner before transfer err=%v, want not found", err)
	}
	if err := db.Model(&Customer{}).Where("tenant_id=? AND id=?", tenant, customer.ID).Updates(map[string]any{"owner_user_id": "new-seller", "owner_org_id": "org-2", "version": gorm.Expr("version+1")}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, oldPrincipal, customer.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old owner after transfer err=%v, want not found", err)
	}
	got, err := repo.FindByID(ctx, newPrincipal, customer.ID, false)
	if err != nil {
		t.Fatalf("new owner after transfer: %v", err)
	}
	if got.CreatedBy != "old-seller" || got.OwnerUserID != "new-seller" {
		t.Fatalf("created_by history or current owner changed: %+v", got)
	}
}
