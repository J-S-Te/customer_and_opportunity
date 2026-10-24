package opportunity

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type frozenOpportunityChecker struct{ userID string }

func (checker frozenOpportunityChecker) ActiveFreezeUser(_ context.Context, _, userID string) (bool, error) {
	return userID == checker.userID, nil
}

type opportunityDirectoryStub struct {
	users map[string]ownerdirectory.User
}

func (stub opportunityDirectoryStub) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	return ownerdirectory.Page{Items: []ownerdirectory.User{stub.users["existing"], stub.users["frozen-new"]}}, nil
}
func (stub opportunityDirectoryStub) Validate(context.Context, string, string) error { return nil }
func (stub opportunityDirectoryStub) Resolve(_ context.Context, ids []string) (map[string]ownerdirectory.User, error) {
	result := map[string]ownerdirectory.User{}
	for _, id := range ids {
		if user, ok := stub.users[id]; ok {
			result[id] = user
		}
	}
	return result, nil
}

func TestOpportunityTeamAddRejectsFrozenMemberOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tenant := "qa-opportunity-freeze-" + now.Format("20060102150405.000000000")
	opportunity := &Opportunity{Model: database.Model{TenantID: tenant, CreatedBy: "owner", UpdatedBy: "owner", Version: 1, CreatedAt: now, UpdatedAt: now}, OpportunityNo: "OPP-" + now.Format("150405.000000"), Name: "冻结验收商机", CustomerID: 1, Type: "NEW", Source: "INBOUND", ExpectedAmount: decimal.NewFromInt(100), ExpectedSignDate: now, RequirementSummary: "测试", OwnerUserID: "owner", OwnerOrgID: "org-1", CurrentStage: StageRequirement, Status: StatusFollowing, ContractLinkStatus: "PENDING", ContractSyncVersion: 1, TerminalPendingType: "NONE", StageChangedAt: now}
	if err = db.Create(opportunity).Error; err != nil {
		t.Fatal(err)
	}
	member := Member{Model: database.Model{TenantID: tenant, CreatedBy: "owner", UpdatedBy: "owner", Version: 1, CreatedAt: now, UpdatedAt: now}, OpportunityID: opportunity.ID, UserID: "existing", Role: "sales_support", IsActive: true}
	if err = db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, NewGORMRepository(db), audit.NewGORMWriter(db), nil)
	service.UseOwnerDirectory(opportunityDirectoryStub{users: map[string]ownerdirectory.User{
		"existing":   {ID: "existing", DisplayName: "现有成员"},
		"frozen-new": {ID: "frozen-new", DisplayName: "冻结成员"},
	}})
	service.UseFreezeChecker(frozenOpportunityChecker{userID: "frozen-new"})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "operator", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll, Permissions: map[string]struct{}{"opportunity.team.manage": {}}})
	_, err = service.ReplaceMembers(ctx, opportunity.ID, ReplaceMembersRequest{
		Members: []TeamMemberInput{{UserID: "existing", Role: "sales_support"}, {UserID: "frozen-new", Role: "sales_support"}},
		Version: 1, Reason: "新增团队成员",
	})
	if !errors.Is(err, ErrOwnerFrozenByHandover) {
		t.Fatalf("replace members err=%v, want ErrOwnerFrozenByHandover", err)
	}
	var count int64
	if err = db.Model(&Member{}).Where("tenant_id=? AND opportunity_id=?", tenant, opportunity.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rejected team update created members: count=%d", count)
	}
	var stored Opportunity
	if err = db.First(&stored, opportunity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 {
		t.Fatalf("rejected team update advanced version: %d", stored.Version)
	}
}
