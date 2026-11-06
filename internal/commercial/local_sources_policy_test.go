package commercial

import (
	core "github.com/J-S-Te/license-core"
	"testing"
)

// TestLocalSourceHistoryPolicy 验证新增独立来源只读路径的许可分类精确且不覆盖写入。
// 参数：t为测试上下文，路由取自真实Gin注册模板。
// 返回值：无；历史读取误归写入或未知写入被放行时失败。
func TestLocalSourceHistoryPolicy(t *testing.T) {
	paths := []string{"/api/v1/local-admin/customers", "/api/v1/local-admin/customers/:id/contacts", "/api/v1/local-admin/customers/:id/accounts", "/api/v1/local-admin/customers/:id/invitations", "/api/v1/local-admin/customers/:id/projects", "/api/v1/local-admin/customers/:id/project-access", "/api/v1/local-admin/operations"}
	for _, path := range paths {
		if Operation("portal", "GET", path) != core.READ_HISTORY {
			t.Errorf("not historical: %s", path)
		}
		if Operation("portal", "POST", path) != core.MUTATE_BUSINESS {
			t.Errorf("write escaped: %s", path)
		}
		if Operation("crm", "GET", path) != core.MUTATE_BUSINESS {
			t.Errorf("cross-product route accepted: %s", path)
		}
	}
	if Operation("crm", "GET", "/api/v1/opportunities/:id/win-confirmation") != core.READ_HISTORY {
		t.Error("CRM evidence history blocked")
	}
	for _, path := range []string{"/api/v1/local-admin/unknown", "/api/v1/local-admin/operations/:number/retry", "/api/v1/local-admin/operations/:number/report-file"} {
		if Operation("portal", "GET", path) != core.MUTATE_BUSINESS {
			t.Errorf("unknown route escaped: %s", path)
		}
	}
}
