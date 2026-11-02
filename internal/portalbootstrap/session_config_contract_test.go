package portalbootstrap

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestLoadConfigUsesManagedPortalSessionKeys 核验真实启动入口读取平台下发的门户专属配置。
// 参数：t为隔离进程环境的测试上下文。
// 返回值：无；开关或公开桥接地址被忽略即失败。
func TestLoadConfigUsesManagedPortalSessionKeys(t *testing.T) {
	setPortalLoadConfigFixture(t)
	t.Setenv("PORTAL_CENTRAL_SESSION_ENABLED", "true")
	t.Setenv("PORTAL_CENTRAL_SESSION_PUBLIC_BASE_URL", "https://foundation.example")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.CentralSessionEnabled || config.CentralSessionPublicBaseURL != "https://foundation.example" {
		t.Fatalf("managed session configuration ignored: enabled=%v origin=%q",
			config.CentralSessionEnabled, config.CentralSessionPublicBaseURL)
	}
}

// TestLoadConfigRejectsInvalidManagedPortalSessionFlag 核验门户专属开关拼写错误不能静默降级。
// 参数：t为启动配置测试上下文。
// 返回值：无；非法值未返回对应配置错误即失败。
func TestLoadConfigRejectsInvalidManagedPortalSessionFlag(t *testing.T) {
	setPortalLoadConfigFixture(t)
	t.Setenv("PORTAL_CENTRAL_SESSION_ENABLED", "treu")
	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "PORTAL_CENTRAL_SESSION_ENABLED") {
		t.Fatalf("malformed managed flag must fail startup, got %v", err)
	}
}

// TestLoadConfigDoesNotInheritCRMSessionFlag 保证独立门户不会误用CRM的无前缀开关。
// 参数：t为隔离环境测试上下文。
// 返回值：无；门户专属关闭配置被其他系统覆盖即失败。
func TestLoadConfigDoesNotInheritCRMSessionFlag(t *testing.T) {
	setPortalLoadConfigFixture(t)
	t.Setenv("CENTRAL_SESSION_ENABLED", "true")
	t.Setenv("PORTAL_CENTRAL_SESSION_ENABLED", "false")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.CentralSessionEnabled {
		t.Fatal("CRM session flag enabled the independent portal")
	}
}

// setPortalLoadConfigFixture 提供完整但不连接任何服务的合成启动配置。
// 参数：t为负责恢复环境的测试上下文。
// 返回值：无；所有凭据仅为固定测试标记。
func setPortalLoadConfigFixture(t *testing.T) {
	t.Helper()
	config := validPortalConfig()
	values := map[string]string{
		"CENTRAL_SESSION_ENABLED": "false", "CENTRAL_SESSION_PUBLIC_BASE_URL": "",
		"PORTAL_CENTRAL_SESSION_ENABLED": "false", "PORTAL_CENTRAL_SESSION_PUBLIC_BASE_URL": "",
		"PORTAL_MYSQL_DSN": config.MySQLDSN, "PORTAL_PUBLIC_ORIGIN": config.PublicOrigin,
		"PORTAL_OIDC_TENANT_ID": config.TenantID, "PORTAL_ROLE_CONFIG_HASH": config.RoleConfigHash,
		"PORTAL_OIDC_ISSUER": config.OIDCIssuer, "PORTAL_OIDC_CLIENT_ID": config.OIDCClientID,
		"PORTAL_OIDC_CLIENT_SECRET":                  config.OIDCClientSecret,
		"PORTAL_OIDC_REDIRECT_URI":                   config.OIDCRedirectURI,
		"PORTAL_ACCOUNT_SECURITY_CENTER_URL":         config.AccountSecurityCenterURL,
		"PORTAL_MACHINE_TOKEN_ISSUER":                config.MachineTokenIssuer,
		"PORTAL_MACHINE_TOKEN_AUDIENCE":              config.MachineTokenAudience,
		"PORTAL_MACHINE_TOKEN_PUBLIC_KEY_PATH":       config.MachineTokenPublicKeyPath,
		"PORTAL_CRM_PROVISION_CLIENT_SUBJECT":        config.CRMProvisionClientSubject,
		"PORTAL_CRM_DISABLE_CLIENT_SUBJECT":          config.CRMDisableClientSubject,
		"PORTAL_CRM_INVITE_BASE_URL":                 config.CRMInviteBaseURL,
		"PORTAL_CRM_INVITE_TOKEN_URL":                config.CRMInviteTokenURL,
		"PORTAL_CRM_INVITE_CLIENT_ID":                config.CRMInviteClientID,
		"PORTAL_CRM_INVITE_CLIENT_SECRET":            config.CRMInviteClientSecret,
		"PORTAL_PLATFORM_BASE_URL":                   config.PlatformBaseURL,
		"PLATFORM_APPLICATION_CODE":                  config.PlatformApplicationCode,
		"PLATFORM_ENVIRONMENT_CODE":                  config.PlatformEnvironmentCode,
		"PLATFORM_AUDIT_CLIENT_ID":                   config.PlatformAuditClientID,
		"PLATFORM_AUDIT_CLIENT_SECRET":               config.PlatformAuditClientSecret,
		"PORTAL_ENCRYPTION_KEY_BASE64":               base64.StdEncoding.EncodeToString(config.EncryptionKey),
		"PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64": base64.StdEncoding.EncodeToString(config.ReportIngestDescriptorKey),
		"PORTAL_HMAC_KEY_BASE64":                     base64.StdEncoding.EncodeToString(config.HMACKey),
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
