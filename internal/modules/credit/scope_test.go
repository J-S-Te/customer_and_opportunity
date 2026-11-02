package credit

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本仓库没有 sqlite 测试驱动，信用子模块沿用已有的 MySQL DryRun 基建：GORM 只编译 SQL、
// 不建立连接（DisableAutomaticPing + DryRun），因此下面的用例在无数据库环境下也能校验
// 真实服务方法生成的可见性谓词，不会接触任何业务库。
const creditDryRunDSN = "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local"

func creditDryRunDB(t *testing.T, log logger.Interface) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       creditDryRunDSN,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// creditSQLRecorder 捕获 DryRun 模式下每条语句展开后的 SQL（占位符已替换为实际入参），
// 用于断言服务方法真正下发给数据库的谓词，而不是只断言 helper 拼出来的片段。
type creditSQLRecorder struct {
	logger.Interface
	queries []string
}

func newCreditSQLRecorder() *creditSQLRecorder {
	return &creditSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
}

func (r *creditSQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if fc == nil {
		return
	}
	if sql, _ := fc(); strings.TrimSpace(sql) != "" {
		r.queries = append(r.queries, sql)
	}
}

func (r *creditSQLRecorder) firstQueryContaining(t *testing.T, substrings ...string) string {
	t.Helper()
	for _, query := range r.queries {
		matched := true
		for _, substring := range substrings {
			if !strings.Contains(query, substring) {
				matched = false
				break
			}
		}
		if matched {
			return query
		}
	}
	t.Fatalf("没有捕获到同时包含 %v 的语句：%#v", substrings, r.queries)
	return ""
}

// salesOnlyPrincipal 构造纯销售主体：即使平台下发了 application/tenant 级数据范围
// （ScopeAll），CRM 客户可见范围也只能是本人当前负责的客户。
func salesOnlyPrincipal(userID string) auth.Principal {
	return auth.Principal{TenantID: "tenant-a", UserID: userID, Roles: []string{"sales"}, ScopeMode: auth.ScopeAll}
}

func creditScopeStatement(t *testing.T, principal auth.Principal) *gorm.Statement {
	t.Helper()
	return scopedCreditCustomers(creditDryRunDB(t, nil), principal).
		Where("crm_customers.id=?", 7).
		Find(&[]struct{ ID uint64 }{}).Statement
}

// 场景：同一销售 sales-a 面对两个客户。
//   - 客户 A：created_by=sales-a，owner_user_id=other-seller（已交接给他人）；
//   - 客户 B：created_by=other-seller，owner_user_id=sales-a（当前负责）。
//
// 销售范围只能按 owner_user_id 收窄，所以 A 不满足谓词、不可见，B 满足谓词、可见。
// 一旦谓词退回 created_by，A 会重新对 sales-a 可见而 B 消失，本用例随即失败。
func TestCreditSalesScopeFollowsCurrentOwnerNotCreator(t *testing.T) {
	statement := creditScopeStatement(t, salesOnlyPrincipal("sales-a"))
	sql := statement.SQL.String()
	if strings.Contains(sql, "created_by") {
		t.Fatalf("销售客户范围不得回退到创建人：%q", sql)
	}
	if !strings.Contains(sql, "crm_customers.owner_user_id = ?") {
		t.Fatalf("销售客户范围必须限制为当前负责人：%q", sql)
	}
	if len(statement.Vars) < 2 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != "sales-a" {
		t.Fatalf("销售客户范围参数=%#v", statement.Vars)
	}
}

// GetLevel（GET /customers/:id/credit）必须走同一个按当前负责人收窄的谓词。
// 客户 A（created_by=sales-a、owner=other-seller）虽然由 sales-a 创建，也不能进入
// 结果集；客户 B（created_by=other-seller、owner=sales-a）才会。DryRun 不执行行过滤，
// 因此这里同时断言 A、B 两次服务调用下发的语句都只按 owner_user_id 收窄——两者
// 共用同一谓词、差异只在数据行，换成 created_by 时 A/B 的可见性会整体对调。
func TestGetLevelScopesSalesByCurrentOwner(t *testing.T) {
	recorder := newCreditSQLRecorder()
	service := NewService(creditDryRunDB(t, recorder))
	ctx := auth.WithPrincipal(context.Background(), salesOnlyPrincipal("sales-a"))

	for _, customerID := range []uint64{101, 102} {
		if _, err := service.GetLevel(ctx, customerID); err != nil {
			t.Fatalf("DryRun GetLevel(%d) 返回错误：%v", customerID, err)
		}
		query := recorder.firstQueryContaining(t,
			"FROM `crm_customers`",
			"crm_customers.owner_user_id = 'sales-a'",
			fmt.Sprintf("crm_customers.id=%d", customerID),
		)
		if strings.Contains(query, "created_by") {
			t.Fatalf("信用等级查询泄露了创建人范围：%q", query)
		}
	}
}

// History（GET /customers/:id/credit/history）把可见性作为子查询嵌进真实读取语句，
// 交接后旧负责人读取原客户信用历史必须被同一 owner 谓词挡掉。
func TestHistoryEmbedsCurrentOwnerScope(t *testing.T) {
	recorder := newCreditSQLRecorder()
	service := NewService(creditDryRunDB(t, recorder))
	ctx := auth.WithPrincipal(context.Background(), salesOnlyPrincipal("sales-a"))

	if _, err := service.History(ctx, 101, 1, 20); err != nil {
		t.Fatalf("DryRun History 返回错误：%v", err)
	}
	query := recorder.firstQueryContaining(t, "customer_id IN (SELECT", "crm_customers.owner_user_id = 'sales-a'")
	if strings.Contains(query, "created_by") {
		t.Fatalf("信用历史查询泄露了创建人范围：%q", query)
	}
	if !strings.Contains(query, "crm_customers.id=101") {
		t.Fatalf("信用历史子查询丢失客户条件：%q", query)
	}
}

// 管理角色与未知范围必须保持原有语义：只有纯销售才收窄到 owner_user_id。
func TestCreditCustomerScopePreservesManagerOrganizationScope(t *testing.T) {
	statement := creditScopeStatement(t, auth.Principal{
		TenantID:        "tenant-a",
		UserID:          "director-a",
		Roles:           []string{"sales", "sales_director"},
		ScopeMode:       auth.ScopeOrg,
		OrganizationIDs: []string{"org-a", "org-b"},
	})
	if sql := statement.SQL.String(); !strings.Contains(sql, "crm_customers.owner_org_id IN") || strings.Contains(sql, "crm_customers.owner_user_id") {
		t.Fatalf("管理角色组织范围被改变：%q", sql)
	}
}

func TestCreditCustomerScopeFailsClosedForEmptyOrganizationScope(t *testing.T) {
	statement := creditScopeStatement(t, auth.Principal{TenantID: "tenant-a", UserID: "director-a", ScopeMode: auth.ScopeOrg})
	if !strings.Contains(statement.SQL.String(), "1 = 0") {
		t.Fatalf("空组织范围不得暴露租户客户：%q", statement.SQL.String())
	}
}

func TestCreditCustomerScopePreservesAllAndSelfModesForNonSalesRoles(t *testing.T) {
	all := creditScopeStatement(t, auth.Principal{TenantID: "tenant-a", UserID: "admin-a", Roles: []string{"crm_super_admin"}, ScopeMode: auth.ScopeAll})
	if sql := all.SQL.String(); strings.Contains(sql, "crm_customers.owner_user_id") || strings.Contains(sql, "crm_customers.owner_org_id") {
		t.Fatalf("管理员 ALL 范围被收窄：%q", sql)
	}

	self := creditScopeStatement(t, auth.Principal{TenantID: "tenant-a", UserID: "reader-a", ScopeMode: auth.ScopeSelf})
	if !strings.Contains(self.SQL.String(), "crm_customers.owner_user_id") {
		t.Fatalf("非销售个人范围必须保持按负责人收窄：%q", self.SQL.String())
	}
}

// creditSalesOnlyPrincipal 仍用于信用调整申请/撤回的角色闸门，语义不得随范围复用而漂移。
func TestCreditSalesOnlyPrincipalRoleMatrix(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{[]string{"sales"}, true},
		{[]string{"sales", "sales_director"}, false},
		{[]string{"sales", "customer_admin"}, false},
		{[]string{"sales", "crm_super_admin"}, false},
		{[]string{"sales", "auditor"}, false},
		{[]string{"sales", "admin"}, false},
		{[]string{"sales_director"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := creditSalesOnlyPrincipal(auth.Principal{Roles: tc.roles}); got != tc.want {
			t.Fatalf("creditSalesOnlyPrincipal(%v)=%v, want %v", tc.roles, got, tc.want)
		}
	}
}
