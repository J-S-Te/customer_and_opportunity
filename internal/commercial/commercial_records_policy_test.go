package commercial

import (
	"testing"

	core "github.com/J-S-Te/license-core"
)

// TestCommercialRecordHistoryPolicy 验证人工资料只有精确只读列表属于历史读取。
// 参数：t 为许可分类测试上下文，路径为正式路由模板。
// 返回值：无；任何写入或未知、跨产品路径被当成历史读取时失败。
func TestCommercialRecordHistoryPolicy(t *testing.T) {
	list := "/api/v1/opportunities/:id/commercial-records"
	if Operation("crm", "GET", list) != core.READ_HISTORY {
		t.Error("人工历史资料列表不能误归业务写入")
	}
	for _, command := range []struct{ method, path string }{
		{"POST", list}, {"PUT", list + "/:recordID"}, {"POST", list + "/:recordID/void"},
		{"GET", list + "/unknown"},
	} {
		if Operation("crm", command.method, command.path) != core.MUTATE_BUSINESS {
			t.Errorf("业务写入或未知路由被误放为历史：%s %s", command.method, command.path)
		}
	}
	if Operation("portal", "GET", list) != core.MUTATE_BUSINESS {
		t.Error("人工CRM资料路径不能扩展门户许可范围")
	}
}
