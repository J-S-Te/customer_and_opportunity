package portalaccessdisableworker

import "testing"

// TestBindingScopeMatchesRegisteredCredential 验证平台绑定停用只请求实际登记的点号scope。
// 参数：t为纯配置测试，不连接数据库或OAuth。
// 返回值：无；默认scope错误或接受用途名/双scope时失败。
func TestBindingScopeMatchesRegisteredCredential(t *testing.T) {
	setValidConfig(t)
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_ONLY", "true")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_BASE_URL", "https://identity.example/api/v1/internal/external-users")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_CLIENT_ID", "binding-disabler")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_CLIENT_SECRET", "fixture")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_SCOPE", "")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Binding.Scope != "portal.identity_mapping.disable" {
		t.Fatalf("default binding scope=%q", c.Binding.Scope)
	}
	for _, scope := range []string{"portal_mapping_disable", "portal.identity_mapping.disable portal_mapping_disable"} {
		t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_SCOPE", scope)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("unregistered scope accepted %q", scope)
		}
	}
}
