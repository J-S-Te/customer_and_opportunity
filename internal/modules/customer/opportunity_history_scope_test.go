package customer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 商机历史（GET /customers/:id/opportunities）是客户子资源，但条目来自商机聚合：
// 客户可见只证明父客户可见，条目本身必须再按商机数据范围收窄。下面的用例沿用仓库已有的
// MySQL DryRun 基建（GORM 只编译 SQL、不建立连接），断言仓储真正下发的范围谓词。
const opportunityHistoryDryRunDSN = "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local"

func opportunityHistoryDryRunDB(t *testing.T, log logger.Interface) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       opportunityHistoryDryRunDSN,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// opportunityHistorySQLRecorder 捕获 DryRun 语句展开后的 SQL（占位符已替换为实际入参），
// 用于断言列表查询与计数查询都带上了商机范围谓词。
type opportunityHistorySQLRecorder struct {
	logger.Interface
	queries []string
}

func newOpportunityHistorySQLRecorder() *opportunityHistorySQLRecorder {
	return &opportunityHistorySQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
}

func (r *opportunityHistorySQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if fc == nil {
		return
	}
	if sql, _ := fc(); strings.TrimSpace(sql) != "" {
		r.queries = append(r.queries, sql)
	}
}

func (r *opportunityHistorySQLRecorder) queriesContaining(t *testing.T, substrings ...string) []string {
	t.Helper()
	matched := make([]string, 0, len(r.queries))
	for _, query := range r.queries {
		ok := true
		for _, substring := range substrings {
			if !strings.Contains(query, substring) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, query)
		}
	}
	if len(matched) == 0 {
		t.Fatalf("没有捕获到同时包含 %v 的语句：%#v", substrings, r.queries)
	}
	return matched
}

// TestOpportunityHistoryAppliesOpportunityDataScope 断言商机历史子资源的范围谓词与
// opportunity.ApplyDataScope 完全一致：SELF 按当前负责人、ORG 按组织集合、空组织集合
// 失败收窄（1=0）、ALL 不收窄到任何归属列。
func TestOpportunityHistoryAppliesOpportunityDataScope(t *testing.T) {
	cases := []struct {
		name      string
		principal auth.Principal
		want      []string
		reject    []string
	}{
		{
			name:      "self",
			principal: auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf},
			want:      []string{"crm_opportunities.tenant_id='tenant-a'", "crm_opportunities.owner_user_id='sales-a'"},
			reject:    []string{"owner_org_id"},
		},
		{
			name:      "org",
			principal: auth.Principal{TenantID: "tenant-a", UserID: "director-a", ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a", "org-b"}},
			want:      []string{"crm_opportunities.tenant_id='tenant-a'", "crm_opportunities.owner_org_id IN"},
			reject:    []string{"crm_opportunities.owner_user_id"},
		},
		{
			name:      "org-empty",
			principal: auth.Principal{TenantID: "tenant-a", UserID: "director-a", ScopeMode: auth.ScopeOrg},
			want:      []string{"crm_opportunities.tenant_id='tenant-a'", "1=0"},
			reject:    []string{"crm_opportunities.owner_user_id", "crm_opportunities.owner_org_id"},
		},
		{
			name:      "all",
			principal: auth.Principal{TenantID: "tenant-a", UserID: "admin-a", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll},
			want:      []string{"crm_opportunities.tenant_id='tenant-a'"},
			reject:    []string{"crm_opportunities.owner_user_id", "crm_opportunities.owner_org_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newOpportunityHistorySQLRecorder()
			repo := NewGORMRepository(opportunityHistoryDryRunDB(t, recorder))
			// 明细查询走 Scan，gorm 在 DryRun 下用 ErrDryRunModeUnsupported 表示“只编译不执行”；
			// 该错误是 DryRun 的预期产物（SQL 已被 recorder 捕获），其它任何错误都必须失败。
			if _, err := repo.ListOpportunityHistory(context.Background(), tc.principal, 42, 1, 20); err != nil && !errors.Is(err, gorm.ErrDryRunModeUnsupported) {
				t.Fatalf("DryRun ListOpportunityHistory 返回错误：%v", err)
			}
			// 计数查询与明细查询都必须带范围谓词，缺一就会通过 total 泄露范围外商机数量。
			scoped := recorder.queriesContaining(t, "FROM `crm_opportunities`", "crm_opportunities.customer_id = 42")
			if len(scoped) < 2 {
				t.Fatalf("期望计数与明细两条语句都受范围约束，实际捕获 %d 条：%#v", len(scoped), scoped)
			}
			for _, query := range scoped {
				for _, want := range append([]string{"crm_opportunities.deleted_at IS NULL"}, tc.want...) {
					if !strings.Contains(query, want) {
						t.Fatalf("商机历史缺少范围条件 %q：%q", want, query)
					}
				}
				for _, reject := range tc.reject {
					if strings.Contains(query, reject) {
						t.Fatalf("商机历史出现不该有的范围条件 %q：%q", reject, query)
					}
				}
			}
		})
	}
}

type opportunityHistorySpyRepository struct {
	Repository
	customerVisible  bool
	historyCalls     int
	historyPrincipal auth.Principal
	historyCustomer  uint64
}

func (r *opportunityHistorySpyRepository) FindByID(_ context.Context, _ auth.Principal, _ uint64, _ bool) (*Customer, error) {
	if !r.customerVisible {
		return nil, ErrNotFound
	}
	return &Customer{}, nil
}

func (r *opportunityHistorySpyRepository) ListOpportunityHistory(_ context.Context, principal auth.Principal, customerID uint64, _, _ int) (pagination.Page[OpportunitySummary], error) {
	r.historyCalls++
	r.historyPrincipal = principal
	r.historyCustomer = customerID
	return pagination.Page[OpportunitySummary]{Items: []OpportunitySummary{}, Page: 1, PageSize: 20}, nil
}

// TestListOpportunityHistoryKeepsParentCheckAndForwardsPrincipal 守住两点：父客户可见性
// 校验必须保留（父客户不可见时不得再查子表），以及 principal 必须原样下传到仓储，
// 否则仓储又只能按租户过滤。
func TestListOpportunityHistoryKeepsParentCheckAndForwardsPrincipal(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}
	ctx := auth.WithPrincipal(context.Background(), principal)

	hidden := &opportunityHistorySpyRepository{customerVisible: false}
	if _, err := (&Service{repo: hidden}).ListOpportunityHistory(ctx, 42, 1, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("父客户不可见时 err=%v, want ErrNotFound", err)
	}
	if hidden.historyCalls != 0 {
		t.Fatalf("父客户不可见时仍查询了商机历史：calls=%d", hidden.historyCalls)
	}

	visible := &opportunityHistorySpyRepository{customerVisible: true}
	if _, err := (&Service{repo: visible}).ListOpportunityHistory(ctx, 42, 1, 20); err != nil {
		t.Fatalf("父客户可见时返回错误：%v", err)
	}
	if visible.historyCalls != 1 {
		t.Fatalf("商机历史调用次数=%d, want 1", visible.historyCalls)
	}
	if visible.historyPrincipal.UserID != "sales-a" || visible.historyPrincipal.ScopeMode != auth.ScopeSelf {
		t.Fatalf("principal 未下传到仓储：%+v", visible.historyPrincipal)
	}
	if visible.historyCustomer != 42 {
		t.Fatalf("客户 ID 传递错误：%d", visible.historyCustomer)
	}
}

// TestCustomerOpportunityHistoryExcludesOutOfScopeOpportunitiesOnMySQL 是真实 MySQL 反例：
// 客户 C 归销售 A，客户 C 下的商机 O 归销售 B。A 能看见客户 C（父客户校验通过），
// 但历史条目里不得出现 O；B 只能看见 O；组织范围只见本组织商机。
// 未配置隔离测试 DSN 时跳过，绝不连接业务库。
func TestCustomerOpportunityHistoryExcludesOutOfScopeOpportunitiesOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tenant := "qa-customer-opportunity-history-" + now.Format("20060102150405.000000000")
	customer := &Customer{Model: database.Model{TenantID: tenant, CreatedBy: "sales-a", UpdatedBy: "sales-a", Version: 1, CreatedAt: now, UpdatedAt: now}, CustomerNo: "CUS-OPPH-" + now.Format("150405.000000"), Name: "商机历史范围客户", NormalizedName: "商机历史范围客户", CustomerType: "ENTERPRISE", Industry: "IT", Region: "CN", OwnerUserID: "sales-a", OwnerOrgID: "org-a", Status: StatusActive, CreditLevel: "B", CreditChangeSource: "INITIAL"}
	if err = db.Create(customer).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("tenant_id=? AND id=?", tenant, customer.ID).Delete(&Customer{}).Error })
	newOpportunity := func(owner, org string) *opportunity.Opportunity {
		return &opportunity.Opportunity{
			Model:               database.Model{TenantID: tenant, CreatedBy: owner, UpdatedBy: owner, Version: 1, CreatedAt: now, UpdatedAt: now},
			OpportunityNo:       "OPP-" + owner + "-" + now.Format("150405.000000"),
			Name:                "商机历史范围商机-" + owner,
			CustomerID:          customer.ID,
			Type:                "NEW",
			Source:              "INBOUND",
			ExpectedAmount:      decimal.NewFromInt(100),
			ExpectedSignDate:    now,
			RequirementSummary:  "范围验收",
			OwnerUserID:         owner,
			OwnerOrgID:          org,
			CurrentStage:        opportunity.StageRequirement,
			Status:              opportunity.StatusFollowing,
			ContractLinkStatus:  "PENDING",
			ContractSyncVersion: 1,
			TerminalPendingType: "NONE",
			StageChangedAt:      now,
		}
	}
	ownedByA := newOpportunity("sales-a", "org-a")
	ownedByB := newOpportunity("sales-b", "org-b")
	if err = db.Create(ownedByA).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(ownedByB).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("tenant_id=?", tenant).Delete(&opportunity.Opportunity{}).Error })
	repo := NewGORMRepository(db)
	ctx := context.Background()
	ids := func(page pagination.Page[OpportunitySummary]) []uint64 {
		result := make([]uint64, 0, len(page.Items))
		for _, item := range page.Items {
			result = append(result, item.ID)
		}
		return result
	}
	sellerA := auth.Principal{TenantID: tenant, UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf}
	pageA, err := repo.ListOpportunityHistory(ctx, sellerA, customer.ID, 1, 20)
	if err != nil {
		t.Fatalf("读取销售 A 的商机历史：%v", err)
	}
	if len(pageA.Items) != 1 || pageA.Items[0].ID != ownedByA.ID || pageA.Total != 1 {
		t.Fatalf("销售 A 的商机历史越权：items=%v total=%d, want 只有 %d", ids(pageA), pageA.Total, ownedByA.ID)
	}
	sellerB := auth.Principal{TenantID: tenant, UserID: "sales-b", Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf}
	pageB, err := repo.ListOpportunityHistory(ctx, sellerB, customer.ID, 1, 20)
	if err != nil {
		t.Fatalf("读取销售 B 的商机历史：%v", err)
	}
	if len(pageB.Items) != 1 || pageB.Items[0].ID != ownedByB.ID {
		t.Fatalf("销售 B 的商机历史越权：items=%v, want 只有 %d", ids(pageB), ownedByB.ID)
	}
	directorA := auth.Principal{TenantID: tenant, UserID: "director-a", ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}
	pageOrg, err := repo.ListOpportunityHistory(ctx, directorA, customer.ID, 1, 20)
	if err != nil {
		t.Fatalf("读取组织 org-a 的商机历史：%v", err)
	}
	if len(pageOrg.Items) != 1 || pageOrg.Items[0].ID != ownedByA.ID {
		t.Fatalf("组织 org-a 的商机历史越权：items=%v, want 只有 %d", ids(pageOrg), ownedByA.ID)
	}
	admin := auth.Principal{TenantID: tenant, UserID: "admin-a", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll}
	pageAll, err := repo.ListOpportunityHistory(ctx, admin, customer.ID, 1, 20)
	if err != nil {
		t.Fatalf("读取全租户商机历史：%v", err)
	}
	if len(pageAll.Items) != 2 {
		t.Fatalf("ALL 范围丢失商机：items=%v", ids(pageAll))
	}
}
