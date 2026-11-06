package contractreference

import (
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// contractReferenceDryRunDB 构造只编译 SQL、不建立连接的 GORM 句柄（DryRun）。
// 用途：数据范围谓词用例在无数据库环境下验证生成的 SQL 与绑定值。
func contractReferenceDryRunDB(t *testing.T) *gorm.DB {
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

type renderedQuery struct {
	sql  string
	vars []any
}

func renderActiveCustomer(t *testing.T, principal auth.Principal, id uint64) renderedQuery {
	t.Helper()
	var reference CustomerReference
	statement := activeCustomerQuery(contractReferenceDryRunDB(t), principal, id).Take(&reference).Statement
	return renderedQuery{sql: statement.SQL.String(), vars: append([]any(nil), statement.Vars...)}
}

func renderUsableOpportunity(t *testing.T, principal auth.Principal, customerID, id uint64) renderedQuery {
	t.Helper()
	var reference OpportunityReference
	statement := usableOpportunityQuery(contractReferenceDryRunDB(t), principal, customerID, id).Take(&reference).Statement
	return renderedQuery{sql: statement.SQL.String(), vars: append([]any(nil), statement.Vars...)}
}

func requireSQLFragments(t *testing.T, rendered renderedQuery, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(rendered.sql, fragment) {
			t.Fatalf("SQL 缺少 %q: %q", fragment, rendered.sql)
		}
	}
}

func requireNoSQLFragments(t *testing.T, rendered renderedQuery, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(rendered.sql, fragment) {
			t.Fatalf("SQL 不应包含 %q: %q", fragment, rendered.sql)
		}
	}
}

func bindsValue(rendered renderedQuery, want any) bool {
	for _, value := range rendered.vars {
		if value == want {
			return true
		}
	}
	return false
}

// TestActiveCustomerScopeFollowsCurrentOwnerNotCreator 覆盖交接后的客户引用权限：
// 客户 created_by=old-seller（创建人），交接后 owner_user_id=new-seller。
// 纯销售（即便持有 application/tenant 级数据范围）只能命中本人当前负责的客户，
// 因此原创建人被拒、现负责人放行，且 created_by 绝不能出现在访问谓词中。
func TestActiveCustomerScopeFollowsCurrentOwnerNotCreator(t *testing.T) {
	formerCreator := auth.Principal{TenantID: "tenant-a", UserID: "old-seller", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
	currentOwner := auth.Principal{TenantID: "tenant-a", UserID: "new-seller", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}

	for _, tt := range []struct {
		name      string
		principal auth.Principal
	}{
		{"former-creator-rejected", formerCreator},
		{"current-owner-allowed", currentOwner},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered := renderActiveCustomer(t, tt.principal, 7)
			requireSQLFragments(t, rendered, "crm_customers.owner_user_id = ?")
			requireNoSQLFragments(t, rendered, "created_by")
			if !bindsValue(rendered, tt.principal.UserID) {
				t.Fatalf("负责人谓词未绑定调用者本人: vars=%#v", rendered.vars)
			}
		})
	}

	// 反向确认：两份查询语句完全一致，只在负责人绑定值上不同；
	// 原创建人的查询绑定 old-seller，物理上无法命中 owner_user_id=new-seller 的行。
	former := renderActiveCustomer(t, formerCreator, 7)
	current := renderActiveCustomer(t, currentOwner, 7)
	if former.sql != current.sql {
		t.Fatalf("同一客户的范围语句不应随调用者变化: former=%q current=%q", former.sql, current.sql)
	}
	if !bindsValue(former, "old-seller") || bindsValue(former, "new-seller") {
		t.Fatalf("交接后原创建人仍可能命中现负责人客户: vars=%#v", former.vars)
	}
	if !bindsValue(current, "new-seller") || bindsValue(current, "old-seller") {
		t.Fatalf("现负责人查询未绑定本人: vars=%#v", current.vars)
	}
}

// TestActiveCustomerScopeKeepsTenantActiveAndMergeGuards 回归租户、有效性、合并标记与删除标记
// 约束：跨租户、已停用、已合并或被软删除的客户仍然不可被合同引用。
func TestActiveCustomerScopeKeepsTenantActiveAndMergeGuards(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf}
	rendered := renderActiveCustomer(t, principal, 7)
	requireSQLFragments(t, rendered,
		"crm_customers.tenant_id = ?",
		"crm_customers.deleted_at IS NULL",
		"crm_customers.id = ?",
		"crm_customers.status = ?",
		"crm_customers.merged_into_id IS NULL",
	)
	if !bindsValue(rendered, "tenant-a") || !bindsValue(rendered, uint64(7)) || !bindsValue(rendered, customer.StatusActive) {
		t.Fatalf("租户/客户ID/状态绑定缺失: vars=%#v", rendered.vars)
	}
	tenants := 0
	for _, value := range rendered.vars {
		if value == "tenant-a" {
			tenants++
		}
	}
	if tenants != 1 {
		t.Fatalf("租户绑定只能来自令牌校验后的主体: vars=%#v", rendered.vars)
	}
}

// TestActiveCustomerScopeOrganizationScopeStaysWithinConfirmedOrganizations 验证管理者/管理员
// 的组织范围仍以平台确认的组织集合为界，集合为空时直接拒绝而不是退化为租户全量。
func TestActiveCustomerScopeOrganizationScopeStaysWithinConfirmedOrganizations(t *testing.T) {
	director := auth.Principal{TenantID: "tenant-a", UserID: "director", Roles: []string{"sales", "sales_director"}, ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a", "org-b"}}
	rendered := renderActiveCustomer(t, director, 7)
	requireSQLFragments(t, rendered, "crm_customers.owner_org_id IN")
	requireNoSQLFragments(t, rendered, "crm_customers.owner_user_id")
	if !bindsValue(rendered, "org-a") || !bindsValue(rendered, "org-b") || bindsValue(rendered, "org-c") {
		t.Fatalf("组织范围绑定与确认集合不一致: vars=%#v", rendered.vars)
	}

	noOrganizations := auth.Principal{TenantID: "tenant-a", UserID: "director", Roles: []string{"customer_admin"}, ScopeMode: auth.ScopeOrg}
	empty := renderActiveCustomer(t, noOrganizations, 7)
	requireSQLFragments(t, empty, "1 = 0")
	requireNoSQLFragments(t, empty, "crm_customers.owner_org_id IN", "crm_customers.owner_user_id")
}

// TestActiveCustomerScopeAdministratorKeepsTenantWideAccess 验证管理员与 TENANT/application
// 级范围的行为与改动前一致：只在租户内放宽，不引入负责人谓词，也不因角色而失效。
func TestActiveCustomerScopeAdministratorKeepsTenantWideAccess(t *testing.T) {
	for _, tt := range []struct {
		name      string
		principal auth.Principal
	}{
		{"super-admin", auth.Principal{TenantID: "tenant-a", UserID: "super", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll}},
		{"customer-admin-tenant-scope", auth.Principal{TenantID: "tenant-a", UserID: "admin", Roles: []string{"sales", "customer_admin"}, ScopeMode: auth.ScopeAll}},
		{"auditor-tenant-scope", auth.Principal{TenantID: "tenant-a", UserID: "auditor", Roles: []string{"auditor"}, ScopeMode: auth.ScopeAll}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered := renderActiveCustomer(t, tt.principal, 7)
			requireSQLFragments(t, rendered, "crm_customers.tenant_id = ?", "crm_customers.status = ?", "crm_customers.merged_into_id IS NULL")
			requireNoSQLFragments(t, rendered, "crm_customers.owner_user_id", "crm_customers.owner_org_id", "created_by")
			if !bindsValue(rendered, "tenant-a") {
				t.Fatalf("管理员仍必须受租户约束: vars=%#v", rendered.vars)
			}
		})
	}

	// 管理员持有组织级范围时仍按组织集合收敛，不会放大为租户全量。
	auditorOrg := auth.Principal{TenantID: "tenant-a", UserID: "auditor", Roles: []string{"auditor"}, ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}
	rendered := renderActiveCustomer(t, auditorOrg, 7)
	requireSQLFragments(t, rendered, "crm_customers.owner_org_id IN")
	if !bindsValue(rendered, "org-a") {
		t.Fatalf("组织级管理员绑定缺失: vars=%#v", rendered.vars)
	}
}

// TestActiveCustomerScopeAllowsCustomerReadAllPermission 验证持有 customer.read.all 的纯销售
// 可以引用非本人负责的客户：客户侧可见性与客户模块同口径，按 ApplyReadScope 放开到租户内
// 全部未删除客户；商机侧谓词不受该权限影响（见下方 strict 用例与商机侧用例）。
func TestActiveCustomerScopeAllowsCustomerReadAllPermission(t *testing.T) {
	viewer := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll,
		Permissions: map[string]struct{}{"customer.read.all": {}}}
	rendered := renderActiveCustomer(t, viewer, 7)
	requireSQLFragments(t, rendered,
		"crm_customers.tenant_id = ?",
		"crm_customers.deleted_at IS NULL",
		"crm_customers.id = ?",
		"crm_customers.status = ?",
		"crm_customers.merged_into_id IS NULL",
	)
	requireNoSQLFragments(t, rendered, "crm_customers.owner_user_id", "created_by")
	if !bindsValue(rendered, "tenant-a") || !bindsValue(rendered, uint64(7)) || !bindsValue(rendered, customer.StatusActive) {
		t.Fatalf("租户/客户ID/状态绑定缺失: vars=%#v", rendered.vars)
	}

	// 同一销售不持有该权限时仍是严格口径：只按当前负责人收窄，原有断言不放宽。
	strict := renderActiveCustomer(t, auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}, 7)
	requireSQLFragments(t, strict, "crm_customers.owner_user_id = ?")
	requireNoSQLFragments(t, strict, "created_by")
	if !bindsValue(strict, "sales-a") {
		t.Fatalf("无权限销售未按本人收窄: vars=%#v", strict.vars)
	}
}

// TestUsableOpportunityScopeMatchesOpportunityModulePredicate 验证商机侧范围与 opportunity
// 模块的权威谓词一致：ALL 按租户、ORG 按 owner_org_id（空集合拒绝）、其余按当前负责人；
// 商机模块没有“纯销售强制自管”特例，因此这里也不额外收紧，且只看 owner 不看 created_by。
func TestUsableOpportunityScopeMatchesOpportunityModulePredicate(t *testing.T) {
	self := renderUsableOpportunity(t, auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeSelf}, 7, 9)
	requireSQLFragments(t, self,
		"crm_opportunities.tenant_id = ?",
		"crm_opportunities.customer_id = ?",
		"crm_opportunities.id = ?",
		"crm_opportunities.opp_status <> ?",
		"crm_opportunities.owner_user_id = ?",
	)
	requireNoSQLFragments(t, self, "created_by")
	if !bindsValue(self, "sales-a") || !bindsValue(self, uint64(7)) || !bindsValue(self, uint64(9)) {
		t.Fatalf("商机范围绑定缺失: vars=%#v", self.vars)
	}

	tenantWide := renderUsableOpportunity(t, auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}, 7, 9)
	requireNoSQLFragments(t, tenantWide, "owner_user_id", "owner_org_id", "created_by")

	organization := renderUsableOpportunity(t, auth.Principal{TenantID: "tenant-a", UserID: "director", Roles: []string{"sales", "sales_director"}, ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}, 7, 9)
	requireSQLFragments(t, organization, "crm_opportunities.owner_org_id IN")
	if !bindsValue(organization, "org-a") {
		t.Fatalf("商机组织范围绑定缺失: vars=%#v", organization.vars)
	}

	emptyOrganization := renderUsableOpportunity(t, auth.Principal{TenantID: "tenant-a", UserID: "director", Roles: []string{"sales", "sales_director"}, ScopeMode: auth.ScopeOrg}, 7, 9)
	requireSQLFragments(t, emptyOrganization, "1 = 0")
	requireNoSQLFragments(t, emptyOrganization, "crm_opportunities.owner_org_id IN")
}

// TestCustomerAndOpportunityScopePredicatesAgreeOutsideSalesOverride 固化两侧同口径的分支：
// SELF/默认按负责人、ORG 按组织集合；差异仅限客户模块对纯销售的特例收紧。
func TestCustomerAndOpportunityScopePredicatesAgreeOutsideSalesOverride(t *testing.T) {
	selfPrincipal := auth.Principal{TenantID: "tenant-a", UserID: "operator", Roles: []string{"sales_director"}, ScopeMode: auth.ScopeSelf}
	customerSelf := renderActiveCustomer(t, selfPrincipal, 7)
	opportunitySelf := renderUsableOpportunity(t, selfPrincipal, 7, 9)
	if strings.Contains(customerSelf.sql, "owner_user_id") != strings.Contains(opportunitySelf.sql, "owner_user_id") {
		t.Fatalf("SELF 分支两侧口径不一致: customer=%q opportunity=%q", customerSelf.sql, opportunitySelf.sql)
	}

	organization := auth.Principal{TenantID: "tenant-a", UserID: "director", Roles: []string{"sales_director"}, ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}
	customerOrganization := renderActiveCustomer(t, organization, 7)
	opportunityOrganization := renderUsableOpportunity(t, organization, 7, 9)
	if strings.Contains(customerOrganization.sql, "owner_org_id IN") != strings.Contains(opportunityOrganization.sql, "owner_org_id IN") {
		t.Fatalf("ORG 分支两侧口径不一致: customer=%q opportunity=%q", customerOrganization.sql, opportunityOrganization.sql)
	}

	// 客户模块的销售特例：纯销售即使拿到 tenant 级范围也只看本人负责的客户；
	// 商机模块没有该特例，两侧在此分支上有意不同。
	sales := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
	if !strings.Contains(renderActiveCustomer(t, sales, 7).sql, "crm_customers.owner_user_id = ?") {
		t.Fatal("客户侧纯销售必须收紧到当前负责人")
	}
	if strings.Contains(renderUsableOpportunity(t, sales, 7, 9).sql, "owner_user_id") {
		t.Fatal("商机侧不应引入客户模块的纯销售特例")
	}
}
