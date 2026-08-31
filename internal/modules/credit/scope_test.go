package credit

import (
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func creditScopeDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func creditScopeStatement(t *testing.T, principal auth.Principal) *gorm.Statement {
	t.Helper()
	return scopedCreditCustomers(creditScopeDryRunDB(t), principal).
		Where("customer.id=?", 7).
		Find(&[]struct{ ID uint64 }{}).Statement
}

func TestCreditCustomerScopeForSalesUsesCreatorEvenWithPlatformAllScope(t *testing.T) {
	statement := creditScopeStatement(t, auth.Principal{
		TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll,
	})
	if sql := statement.SQL.String(); !strings.Contains(sql, "customer.created_by") || strings.Contains(sql, "customer.owner_user_id") {
		t.Fatalf("sales customer scope must use creator: %q", sql)
	}
	if len(statement.Vars) < 3 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != "sales-a" || statement.Vars[2] != 7 {
		t.Fatalf("vars=%#v", statement.Vars)
	}
}

func TestCreditCustomerScopePreservesManagerOrganizationScope(t *testing.T) {
	statement := creditScopeStatement(t, auth.Principal{
		TenantID:        "tenant-a",
		UserID:          "director-a",
		Roles:           []string{"sales", "sales_director"},
		ScopeMode:       auth.ScopeOrg,
		OrganizationIDs: []string{"org-a", "org-b"},
	})
	if sql := statement.SQL.String(); !strings.Contains(sql, "customer.owner_org_id IN") || strings.Contains(sql, "customer.created_by") {
		t.Fatalf("manager organization scope changed: %q", sql)
	}
}

func TestCreditCustomerScopeFailsClosedForEmptyOrganizationScope(t *testing.T) {
	statement := creditScopeStatement(t, auth.Principal{TenantID: "tenant-a", UserID: "director-a", ScopeMode: auth.ScopeOrg})
	if !strings.Contains(statement.SQL.String(), "1=0") {
		t.Fatalf("empty organization scope must not expose tenant customers: %q", statement.SQL.String())
	}
}

func TestCreditCustomerScopePreservesAllAndSelfModesForNonSalesRoles(t *testing.T) {
	all := creditScopeStatement(t, auth.Principal{TenantID: "tenant-a", UserID: "admin-a", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll})
	if sql := all.SQL.String(); strings.Contains(sql, "customer.created_by") || strings.Contains(sql, "customer.owner_user_id") || strings.Contains(sql, "customer.owner_org_id") {
		t.Fatalf("admin ALL scope was narrowed: %q", sql)
	}

	self := creditScopeStatement(t, auth.Principal{TenantID: "tenant-a", UserID: "reader-a", ScopeMode: auth.ScopeSelf})
	if !strings.Contains(self.SQL.String(), "customer.owner_user_id") {
		t.Fatalf("non-sales SELF scope must preserve existing owner rule: %q", self.SQL.String())
	}
}
