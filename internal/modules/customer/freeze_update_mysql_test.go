package customer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type frozenCustomerChecker struct{ userID string }

func (checker frozenCustomerChecker) ActiveFreezeUser(_ context.Context, _, userID string) (bool, error) {
	return userID == checker.userID, nil
}

func TestCustomerUpdateOwnerChangeRejectsFrozenOwnerOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := security.NewSensitiveCodec([]byte("01234567890123456789012345678901"), []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tenant := "qa-customer-freeze-" + now.Format("20060102150405.000000000")
	customer := &Customer{Model: database.Model{TenantID: tenant, CreatedBy: "owner", UpdatedBy: "owner", Version: 1, CreatedAt: now, UpdatedAt: now}, CustomerNo: "CUS-" + now.Format("150405.000000"), Name: "冻结验收客户", NormalizedName: "冻结验收客户", CustomerType: "ENTERPRISE", Industry: "IT", Region: "CN", OwnerUserID: "owner", OwnerOrgID: "org-1", Status: StatusActive, CreditLevel: "B", CreditChangeSource: "INITIAL"}
	if err = db.Create(customer).Error; err != nil {
		t.Fatal(err)
	}
	contact := Contact{CustomerID: customer.ID, Name: "联系人", PhoneCipher: []byte("cipher"), PhoneMasked: "138****0000", IsRegistration: true, SortOrder: 0}
	if err = db.Create(&contact).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, NewGORMRepository(db), audit.NewGORMWriter(db), codec)
	service.UseFreezeChecker(frozenCustomerChecker{userID: "frozen-owner"})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "operator", Roles: []string{"customer_admin"}, ScopeMode: auth.ScopeAll, Permissions: map[string]struct{}{"customer.update": {}}})
	_, err = service.Update(ctx, customer.ID, UpdateRequest{
		Name: "冻结验收客户", CustomerType: "ENTERPRISE", Industry: "IT", Region: "CN",
		OwnerUserID: "frozen-owner", OwnerOrgID: "org-2", Version: 1, Reason: "更换负责人",
		Contacts: []UpdateContactInput{{Name: "联系人", IsRegistration: true}},
	})
	if !errors.Is(err, ErrOwnerFrozenByHandover) {
		t.Fatalf("update err=%v, want ErrOwnerFrozenByHandover", err)
	}
	var stored Customer
	if err = db.First(&stored, customer.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.OwnerUserID != "owner" || stored.OwnerOrgID != "org-1" || stored.Version != 1 {
		t.Fatalf("rejected update changed customer: owner=%s org=%s version=%d", stored.OwnerUserID, stored.OwnerOrgID, stored.Version)
	}
}
