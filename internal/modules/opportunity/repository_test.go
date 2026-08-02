package opportunity

import (
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestScopedOpportunityQueryStartsWithTenantAndDataScope(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	tests := []struct {
		name      string
		principal auth.Principal
		wantSQL   string
	}{
		{name: "self", principal: auth.Principal{TenantID: "tenant-a", UserID: "user-a", ScopeMode: auth.ScopeSelf}, wantSQL: "owner_user_id"},
		{name: "org", principal: auth.Principal{TenantID: "tenant-a", ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}, wantSQL: "owner_org_id"},
		{name: "all", principal: auth.Principal{TenantID: "tenant-a", ScopeMode: auth.ScopeAll}, wantSQL: "tenant_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var rows []Opportunity
			statement := scoped(db.Model(&Opportunity{}), test.principal).Find(&rows).Statement
			sql := statement.SQL.String()
			if !strings.Contains(sql, "tenant_id") || !strings.Contains(sql, test.wantSQL) {
				t.Fatalf("scope predicate missing from %q", sql)
			}
		})
	}
}

func TestIdempotencyReplayQueryBindsActor(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	statement := db.Where("tenant_id=? AND opportunity_id=? AND operation=? AND actor_id=? AND idempotency_key=?", "tenant", 7, "OWNER_CHANGE", "actor", "key").Take(&ChangeIdempotency{}).Statement
	for _, predicate := range []string{"tenant_id", "opportunity_id", "operation", "actor_id", "idempotency_key"} {
		if !strings.Contains(statement.SQL.String(), predicate) {
			t.Fatalf("missing %s in %q", predicate, statement.SQL.String())
		}
	}
}

func TestCreateIdempotencyReplayQueryBindsTenantActorAndKey(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	var value CreateIdempotency
	statement := db.Where("tenant_id=? AND actor_id=? AND idempotency_key=?", "tenant", "actor", "key").Take(&value).Statement
	for _, predicate := range []string{"tenant_id", "actor_id", "idempotency_key"} {
		if !strings.Contains(statement.SQL.String(), predicate) {
			t.Fatalf("missing %s in %q", predicate, statement.SQL.String())
		}
	}
}

func TestCustomerVisibilityGuardLocksOnlyActiveUnmergedCustomer(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	query := db.Table("crm_customers").Clauses(clause.Locking{Strength: "SHARE"}).
		Where("tenant_id=? AND id=? AND status=? AND merged_into_id IS NULL AND deleted_at IS NULL", "tenant-a", 7, "ACTIVE")
	var row struct{ ID uint64 }
	statement := query.Select("id").Take(&row).Statement.SQL.String()
	for _, fragment := range []string{"tenant_id", "status", "merged_into_id IS NULL", "deleted_at IS NULL", "FOR SHARE"} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("missing %q in %q", fragment, statement)
		}
	}
}

func TestLatestApprovedQuotationBindsTenantAndExcludesSupersededApproval(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var model ExternalLink
	statement := latestApprovedQuotationQuery(db, "tenant-a", 7).Take(&model).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"tenant_id", "opportunity_id", "type", "status", "NOT EXISTS", "newer.source_id", "newer.changed_at", "newer.id"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing %q in %q", fragment, sql)
		}
	}
}
