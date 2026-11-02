package customer

import (
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
)

// ApplyDataScope 把 crm_customers 查询限制到 principal 可见的客户，是 CRM 客户数据范围的
// 唯一权威实现。任何需要"这个主体能不能看到这个客户"的模块（客户读取、合并、商机父客户校验、
// 合同引用 contractreference 等）都必须复用本谓词，不得各自复制一份，否则交接与角色特例
// 会在多份实现之间再次漂移。
// 参数：db 必须已绑定 crm_customers 表（Model(&Customer{}) 或 Table("crm_customers")，
// 谓词使用全限定列名）；principal 为认证边界输出的主体。
// 返回值：追加了租户、删除标记与归属范围谓词的查询。
func ApplyDataScope(db *gorm.DB, principal auth.Principal) *gorm.DB {
	// 租户条件始终存在；组织范围必须使用令牌中经平台确认的组织集合，空集合按无权限处理，
	// 不能退化为租户全量或信任请求参数中的组织标识。
	db = db.Where("crm_customers.tenant_id = ? AND crm_customers.deleted_at IS NULL", principal.TenantID)
	// 销售角色的客户归属是“当前负责人”，交接后旧负责人必须立即失去访问；
	// created_by 只保留创建历史，不能作为访问依据。即使平台授权上下文带有
	// application/tenant 级数据范围，也不能因此放大 CRM 客户可见范围。
	if salesOnlyPrincipal(principal) {
		return db.Where("crm_customers.owner_user_id = ?", principal.UserID)
	}
	switch principal.ScopeMode {
	case auth.ScopeAll:
		return db
	case auth.ScopeOrg:
		if len(principal.OrganizationIDs) == 0 {
			return db.Where("1 = 0")
		}
		return db.Where("crm_customers.owner_org_id IN ?", principal.OrganizationIDs)
	default:
		return db.Where("crm_customers.owner_user_id = ?", principal.UserID)
	}
}

// salesOnlyPrincipal 判定主体是否只有销售角色：纯销售即便持有 application/tenant 级数据范围，
// 客户可见范围也只限本人当前负责的客户。
func salesOnlyPrincipal(principal auth.Principal) bool {
	hasSales := false
	for _, role := range principal.Roles {
		if role == "sales" {
			hasSales = true
			break
		}
	}
	if !hasSales {
		return false
	}
	for _, role := range principal.Roles {
		switch role {
		case "sales_director", "customer_admin", "crm_super_admin", "auditor", "admin":
			return false
		}
	}
	return true
}
