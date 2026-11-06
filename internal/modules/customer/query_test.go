package customer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func customerDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestValidateListQueryRejectsUnknownValuesAndInvalidRanges(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	tests := []ListQuery{
		{QuickFilter: "HIGH_VALUE"}, {SortBy: "tenant_id"}, {SortOrder: "sideways"},
		{CreatedFrom: ptrTime(now), CreatedTo: ptrTime(now)},
		{LastFollowupFrom: ptrTime(now.Add(time.Hour)), LastFollowupTo: ptrTime(now)},
	}
	for _, input := range tests {
		if _, err := validateListQuery(input, now); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("input=%#v err=%v", input, err)
		}
	}
}

func TestValidateListQueryCanonicalizesWhitelistAndUsesThirtyDayNewWindow(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	result, err := validateListQuery(ListQuery{QuickFilter: " new ", SortBy: " LAST_FOLLOWUP_AT ", SortOrder: " ASC "}, now)
	if err != nil || result.QuickFilter != QuickFilterNew || result.SortBy != "last_followup_at" || result.SortOrder != "asc" || !result.Now.Equal(now) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := result.Now.AddDate(0, 0, -30); !got.Equal(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("window start=%s", got)
	}
}

func TestScopedCustomerSelfAndOwnerFilterCannotExpandVisibility(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "owner-self", ScopeMode: auth.ScopeSelf}
	statement := scopedCustomer(customerDryRunDB(t).Model(&Customer{}), principal).Where("owner_user_id = ?", "other-owner").Find(&[]Customer{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"crm_customers.tenant_id", "crm_customers.owner_user_id", "owner_user_id = ?"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing %q in %q", fragment, sql)
		}
	}
	if len(statement.Vars) < 3 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != "owner-self" || statement.Vars[2] != "other-owner" {
		t.Fatalf("vars=%#v", statement.Vars)
	}
}

func TestScopedCustomerSalesRoleRemainsSelfScopedWhenPlatformScopeIsAll(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
	statement := scopedCustomer(customerDryRunDB(t).Model(&Customer{}), principal).Find(&[]Customer{}).Statement
	if !strings.Contains(statement.SQL.String(), "crm_customers.owner_user_id = ?") {
		t.Fatalf("sales scope missing current owner predicate: %q", statement.SQL.String())
	}
	if len(statement.Vars) < 2 || statement.Vars[1] != "sales-a" {
		t.Fatalf("vars=%#v", statement.Vars)
	}
}

func TestScopedCustomerManagerKeepsExistingOrganizationScope(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "director", Roles: []string{"sales", "sales_director"}, ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}
	statement := scopedCustomer(customerDryRunDB(t).Model(&Customer{}), principal).Find(&[]Customer{}).Statement
	if !strings.Contains(statement.SQL.String(), "crm_customers.owner_org_id IN") || strings.Contains(statement.SQL.String(), "crm_customers.owner_user_id = ?") {
		t.Fatalf("manager scope changed unexpectedly: %q", statement.SQL.String())
	}
}

func TestCustomerListSQLContainsQuickFiltersAndWhitelistedStableSort(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}
	for _, test := range []struct{ quick, fragment string }{
		{QuickFilterNew, "crm_customers.created_at >="}, {QuickFilterWon, "current_stage = '已签约'"}, {QuickFilterFollowupDue, "next_follow_at <="},
	} {
		db := buildCustomerListQuery(customerDryRunDB(t), principal, ListQuery{QuickFilter: test.quick, SortBy: "opportunity_amount_sum", Now: time.Now().UTC()})
		statement := db.Order("customer_opportunity_summary.opportunity_amount_sum ASC").Order("crm_customers.id ASC").Find(&[]Customer{}).Statement
		sql := statement.SQL.String()
		if !strings.Contains(sql, test.fragment) || !strings.Contains(sql, "customer_opportunity_summary.opportunity_amount_sum ASC") || !strings.Contains(sql, "crm_customers.id ASC") {
			t.Fatalf("quick=%s sql=%q", test.quick, sql)
		}
		if !strings.Contains(sql, "crm_customers.owner_user_id") {
			t.Fatalf("SELF scope missing: %q", sql)
		}
		tenantBindings := 0
		for _, value := range statement.Vars {
			if value == "tenant-a" {
				tenantBindings++
			}
		}
		if tenantBindings != 4 {
			t.Fatalf("outer scope and all aggregate sources must be tenant-bound: vars=%#v", statement.Vars)
		}
	}
}

func TestCustomerListSkipsTenantWideSummariesWhenNeitherFilteringNorSortingByThem(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}
	db := buildCustomerListQuery(customerDryRunDB(t), principal, ListQuery{SortBy: "updated_at", Now: time.Now().UTC()})
	statement := db.Order("crm_customers.updated_at DESC").Limit(20).Find(&[]Customer{}).Statement
	sql := statement.SQL.String()
	if strings.Contains(sql, "customer_followup_summary") || strings.Contains(sql, "customer_opportunity_summary") {
		t.Fatalf("ordinary customer list must not build tenant-wide summaries: %q", sql)
	}
	if customerListUsesSummaryJoin(ListQuery{SortBy: "updated_at"}) {
		t.Fatal("updated_at sort must use page-scoped summary enrichment")
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestParseOptionalTimeRequiresRFC3339(t *testing.T) {
	if value, err := parseOptionalTime("2026-08-01"); err == nil || value != nil {
		t.Fatalf("value=%v err=%v", value, err)
	}
	value, err := parseOptionalTime("2026-08-01T08:30:00+08:00")
	if err != nil || value.Format(time.RFC3339) != "2026-08-01T00:30:00Z" {
		t.Fatalf("value=%v err=%v", value, err)
	}
}

// readableScopeSQLRecorder 捕获 DryRun 语句展开后的 SQL（占位符已替换为实际入参），
// 用于断言仓储详情读取真正下发的范围谓词。
type readableScopeSQLRecorder struct {
	logger.Interface
	queries []string
}

func newReadableScopeSQLRecorder() *readableScopeSQLRecorder {
	return &readableScopeSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
}

func (r *readableScopeSQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if fc == nil {
		return
	}
	if sql, _ := fc(); strings.TrimSpace(sql) != "" {
		r.queries = append(r.queries, sql)
	}
}

func (r *readableScopeSQLRecorder) singleQuery(t *testing.T) string {
	t.Helper()
	if len(r.queries) != 1 {
		t.Fatalf("期望恰好捕获一条 SQL，实际 %d 条：%#v", len(r.queries), r.queries)
	}
	return r.queries[0]
}

// readableScopeDryRunDB 构造带 SQL 记录器的 DryRun 句柄：只编译 SQL、不建立连接。
func readableScopeDryRunDB(t *testing.T, recorder *readableScopeSQLRecorder) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: recorder})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestCustomerListReadScopeAllowsCustomerAllViewer 锁定列表与 CSV 导出共用的读取口径：
// 持有 customer.read.all 的主体即使只有 SELF 范围也不再按负责人收窄，但仍受租户与删除标记
// 约束，不能跨租户读取。
func TestCustomerListReadScopeAllowsCustomerAllViewer(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "viewer-a", ScopeMode: auth.ScopeSelf,
		Permissions: map[string]struct{}{"customer.read.all": {}}}
	statement := buildCustomerListQuery(customerDryRunDB(t), principal, ListQuery{SortBy: "updated_at", Now: time.Now().UTC()}).
		Find(&[]Customer{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"crm_customers.tenant_id", "crm_customers.deleted_at IS NULL"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("客户列表缺少 %q：%q", fragment, sql)
		}
	}
	if strings.Contains(sql, "crm_customers.owner_user_id") {
		t.Fatalf("持有 customer.read.all 的列表仍被负责人收窄：%q", sql)
	}
	tenants := 0
	for _, value := range statement.Vars {
		if value == "tenant-a" {
			tenants++
		}
	}
	if tenants != 1 {
		t.Fatalf("租户绑定只能来自令牌校验后的主体：vars=%#v", statement.Vars)
	}
}

// TestCustomerReadableByIDWidensOnlyTheReadPath 锁定仓储详情读取的放宽边界：持有
// customer.read.all 的纯销售（即便平台范围是 ALL）在只读入口不再按负责人收窄，
// 而严格 FindByID（写路径）仍然只看本人当前负责的客户。
func TestCustomerReadableByIDWidensOnlyTheReadPath(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll,
		Permissions: map[string]struct{}{"customer.read.all": {}}}

	readableRecorder := newReadableScopeSQLRecorder()
	if _, err := NewGORMRepository(readableScopeDryRunDB(t, readableRecorder)).
		FindReadableByID(context.Background(), principal, 42, false); err != nil {
		t.Fatalf("DryRun FindReadableByID 返回错误：%v", err)
	}
	readable := readableRecorder.singleQuery(t)
	for _, fragment := range []string{"crm_customers.tenant_id = 'tenant-a'", "crm_customers.deleted_at IS NULL", "crm_customers.id = 42"} {
		if !strings.Contains(readable, fragment) {
			t.Fatalf("只读入口缺少 %q：%q", fragment, readable)
		}
	}
	if strings.Contains(readable, "owner_user_id") {
		t.Fatalf("只读入口仍被负责人收窄：%q", readable)
	}

	strictRecorder := newReadableScopeSQLRecorder()
	if _, err := NewGORMRepository(readableScopeDryRunDB(t, strictRecorder)).
		FindByID(context.Background(), principal, 42, false); err != nil {
		t.Fatalf("DryRun FindByID 返回错误：%v", err)
	}
	strict := strictRecorder.singleQuery(t)
	if !strings.Contains(strict, "crm_customers.owner_user_id = 'sales-a'") {
		t.Fatalf("严格写路径丢失负责人谓词：%q", strict)
	}
}

// TestCustomerReadableByIDSalesWithoutPermissionStaysOwnerScoped 说明放宽是有条件的：
// 未持有 customer.read.all 的销售即便拿到 ALL 范围，只读入口也必须继续按当前负责人收窄。
func TestCustomerReadableByIDSalesWithoutPermissionStaysOwnerScoped(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
	recorder := newReadableScopeSQLRecorder()
	if _, err := NewGORMRepository(readableScopeDryRunDB(t, recorder)).
		FindReadableByID(context.Background(), principal, 42, false); err != nil {
		t.Fatalf("DryRun FindReadableByID 返回错误：%v", err)
	}
	sql := recorder.singleQuery(t)
	if !strings.Contains(sql, "crm_customers.owner_user_id = 'sales-a'") {
		t.Fatalf("无权限销售丢失负责人谓词：%q", sql)
	}
}

// 只读放宽把详情读取拆成 FindByID（严格写路径）与 FindReadableByID（只读放宽）两个入口。
// 下面两个既有测试替身原本只覆写 FindByID 表达“父客户是否可见”，新入口必须沿用同一结论，
// 否则既有断言（父客户不可见时不得查询子表）会退化成嵌入接口的 nil 调用 panic。
func (r *opportunityHistorySpyRepository) FindReadableByID(ctx context.Context, principal auth.Principal, id uint64, preload bool) (*Customer, error) {
	return r.FindByID(ctx, principal, id, preload)
}

func (r *projectHistoryCustomerRepository) FindReadableByID(ctx context.Context, principal auth.Principal, id uint64, preload bool) (*Customer, error) {
	return r.FindByID(ctx, principal, id, preload)
}

// 关键字里的 % 和 _ 必须按字面匹配：转义后 SQL 声明 ESCAPE，绑定参数里不能再出现裸通配符，
// 否则用户输入会让列表/导出退化成全表扫描。普通关键字仍需保持原样。
func TestCustomerListKeywordEscapesLikeWildcards(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}
	tests := []struct {
		name    string
		keyword string
		want    map[string]int
		notWant string
	}{
		{
			name: "wildcards are literal", keyword: `50%_a\b`,
			// 关键字与规范化值相同，四条 LIKE 都绑定同一个转义后的模式。
			want:    map[string]int{`%50\%\_a\\b%`: 4},
			notWant: `%50%_a\b%`,
		},
		{name: "plain keyword unchanged", keyword: "ACME", want: map[string]int{"%ACME%": 2, "%acme%": 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := buildCustomerListQuery(customerDryRunDB(t), principal, ListQuery{Keyword: test.keyword, SortBy: "updated_at", Now: time.Now().UTC()})
			statement := db.Order("crm_customers.updated_at DESC").Limit(20).Find(&[]Customer{}).Statement
			sql := statement.SQL.String()
			if count := strings.Count(sql, `ESCAPE '\\'`); count != 4 {
				t.Fatalf("every LIKE needs ESCAPE, got %d in %q", count, sql)
			}
			seen := map[string]int{}
			for _, value := range statement.Vars {
				if text, ok := value.(string); ok {
					seen[text]++
				}
			}
			for pattern, count := range test.want {
				if seen[pattern] != count {
					t.Fatalf("pattern %q occurrences=%d want %d (vars=%#v)", pattern, seen[pattern], count, statement.Vars)
				}
			}
			if test.notWant != "" && seen[test.notWant] > 0 {
				t.Fatalf("unescaped wildcard leaked into pattern %q", test.notWant)
			}
		})
	}
}
