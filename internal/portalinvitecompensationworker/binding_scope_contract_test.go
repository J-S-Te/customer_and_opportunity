package portalinvitecompensationworker

import "testing"

// TestBindingScopesMatchRegisteredCredentials 验证补偿写入及禁用都只请求登记点号scope。
// 参数：t为纯配置测试，不运行补偿或外部请求。
// 返回值：无；默认、显式用途名或双scope被接受时失败。
func TestBindingScopesMatchRegisteredCredentials(t *testing.T) {
	for name, value := range map[string]string{
		"PORTAL_INVITE_COMPENSATION_MYSQL_DSN":            "fixture:only@tcp(isolated:3306)/unused",
		"PORTAL_INVITE_COMPENSATION_WORKER_ID":            "fixture",
		"PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL": "https://portal.example/internal/accounts/provision",
		"PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL":     "https://identity.example/oauth2/token",
		"PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_ID":     "portal-fixture", "PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_SECRET": "fixture",
		"PORTAL_INVITE_COMPENSATION_PLATFORM_ROLE_ASSIGN_URL": "https://identity.example/api/v1/internal/application-roles",
		"PORTAL_INVITE_COMPENSATION_PLATFORM_TOKEN_URL":       "https://identity.example/oauth2/token",
		"PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_ID":       "role-fixture", "PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_SECRET": "fixture",
		"PORTAL_INVITE_COMPENSATION_PLATFORM_APPLICATION_CODE": "customer_portal",
		"PORTAL_INVITE_COMPENSATION_BINDING_ENABLED":           "true",
		"PORTAL_INVITE_COMPENSATION_BINDING_BASE_URL":          "https://identity.example/api/v1/internal/external-users",
		"PORTAL_INVITE_COMPENSATION_BINDING_TOKEN_URL":         "https://identity.example/oauth2/token",
		"PORTAL_INVITE_COMPENSATION_BINDING_CLIENT_ID":         "binding-fixture", "PORTAL_INVITE_COMPENSATION_BINDING_CLIENT_SECRET": "fixture",
		"PORTAL_INVITE_COMPENSATION_BINDING_APPLICATION_CODE":  "customer_portal",
		"PORTAL_INVITE_COMPENSATION_BINDING_DISABLE_CLIENT_ID": "disable-fixture", "PORTAL_INVITE_COMPENSATION_BINDING_DISABLE_CLIENT_SECRET": "fixture",
		"PORTAL_INVITE_COMPENSATION_BINDING_SCOPE": "", "PORTAL_INVITE_COMPENSATION_BINDING_DISABLE_SCOPE": "",
	} {
		t.Setenv(name, value)
	}
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Binding.Scope != "portal.identity_mapping.provision" || c.Binding.DisableScope != "portal.identity_mapping.disable" {
		t.Fatalf("unregistered defaults: %q / %q", c.Binding.Scope, c.Binding.DisableScope)
	}
	for key, scope := range map[string]string{"PORTAL_INVITE_COMPENSATION_BINDING_SCOPE": "portal_mapping_provision", "PORTAL_INVITE_COMPENSATION_BINDING_DISABLE_SCOPE": "portal_mapping_disable"} {
		t.Setenv(key, scope)
		if _, err := LoadConfig(); err == nil {
			t.Fatal("purpose accepted as scope", key)
		}
		t.Setenv(key, "")
	}
}
