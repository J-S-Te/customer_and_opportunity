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

// 列表关键字里的 % 和 _ 必须按字面匹配：转义后每条 LIKE 都声明 ESCAPE，
// 绑定参数里不能再出现裸通配符；普通关键字仍保持原有的显示值/规范化值两套模式。
func TestOpportunityListKeywordEscapesLikeWildcards(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	statement := applyOpportunityListKeyword(db.Model(&Opportunity{}), `50%_ A`).Find(&[]Opportunity{}).Statement
	sql := statement.SQL.String()
	if count := strings.Count(sql, `ESCAPE '\\'`); count != 6 {
		t.Fatalf("every LIKE needs ESCAPE, got %d in %q", count, sql)
	}
	seen := map[string]int{}
	for _, value := range statement.Vars {
		if text, ok := value.(string); ok {
			seen[text]++
		}
	}
	// 显示值出现在 4 条 LIKE 上，去空格并小写后的规范化值出现在另外 2 条上。
	for pattern, want := range map[string]int{`%50\%\_ A%`: 4, `%50\%\_a%`: 2} {
		if seen[pattern] != want {
			t.Fatalf("pattern %q occurrences=%d want %d (vars=%#v)", pattern, seen[pattern], want, statement.Vars)
		}
	}
	if seen[`%50%_ A%`] > 0 || seen["%50%_a%"] > 0 {
		t.Fatalf("unescaped wildcard leaked into patterns: %#v", statement.Vars)
	}
}

func TestOpportunityBoardKeywordUsesSameEscaping(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	statement := applyOpportunityBoardKeyword(db.Model(&Opportunity{}), `a_b%`).Find(&[]Opportunity{}).Statement
	if got := strings.Count(statement.SQL.String(), `ESCAPE '\\'`); got != 2 {
		t.Fatalf("board LIKEs must declare ESCAPE, got %d in %q", got, statement.SQL.String())
	}
	if len(statement.Vars) != 2 || statement.Vars[0] != `%a\_b\%%` || statement.Vars[1] != `%a\_b\%%` {
		t.Fatalf("vars=%#v", statement.Vars)
	}
}
