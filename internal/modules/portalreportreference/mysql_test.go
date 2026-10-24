//go:build portal_report_mysql

package portalreportreference

import (
	"context"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMySQLAuthoritativeWatermarkIdentityRequiresExactActiveCustomerSubjectAndUsedInvitation(t *testing.T) {
	dsn := os.Getenv("UIP_P1_CRM_REPORT_IDENTITY_TEST_DSN")
	parsed, err := driver.ParseDSN(dsn)
	if err != nil || parsed.DBName != "crm_identity_p1_test_20261013" {
		t.Fatal("only the explicitly owned disposable CRM schema is permitted")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated identity DB")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&customer.Customer{}, &portalinvite.IdentityLink{}, &portalinvite.Invite{}); err != nil {
		t.Fatal("create isolated identity fixtures", err)
	}
	tenant := "qa-identity-" + time.Now().UTC().Format("150405.000000000")
	c := customer.Customer{Name: "实际客户测试单位", CustomerNo: tenant, NormalizedName: "qa", Status: customer.StatusActive}
	c.TenantID = tenant
	c.Version = 1
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	link := portalinvite.IdentityLink{CustomerID: c.ID, ContactID: 1, PlatformUserID: "subject-1", PortalAccountID: "portal-account", Status: "ACTIVE", ProvisionedAt: time.Now().UTC()}
	link.TenantID = tenant
	link.Version = 1
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	usedAt := time.Now().UTC()
	invite := portalinvite.Invite{InviteNo: tenant, CustomerID: c.ID, ContactID: 1, PlatformUserID: link.PlatformUserID, AccountNo: "13800000090", PortalAccountID: link.PortalAccountID, TokenHash: tenant, Status: portalinvite.StatusUsed, UsedAt: &usedAt, ExpiresAt: usedAt.Add(time.Hour)}
	invite.TenantID = tenant
	invite.Version = 1
	if err := db.Create(&invite).Error; err != nil {
		t.Fatal(err)
	}
	reader := GORMReader{DB: db}
	result, err := reader.Resolve(context.Background(), tenant, c.ID, link.PlatformUserID)
	if err != nil || result.CustomerName != c.Name || result.AccountMasked != "13*******90" || result.AccountID != link.PlatformUserID {
		t.Fatal("wrong authoritative identity", err)
	}
	for _, scope := range []struct {
		tenant   string
		customer uint64
		subject  string
	}{{"another-tenant", c.ID, link.PlatformUserID}, {tenant, c.ID + 1, link.PlatformUserID}, {tenant, c.ID, "another-subject"}} {
		if _, err := reader.Resolve(context.Background(), scope.tenant, scope.customer, scope.subject); err == nil {
			t.Fatal("cross-scope identity resolved")
		}
	}
	if err := db.Model(&link).Update("status", "DISABLED").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Resolve(context.Background(), tenant, c.ID, link.PlatformUserID); err == nil {
		t.Fatal("disabled identity resolved")
	}
}
