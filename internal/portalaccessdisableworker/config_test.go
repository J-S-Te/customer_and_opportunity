package portalaccessdisableworker

import "testing"

func setValidConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PORTAL_ACCESS_DISABLE_MYSQL_DSN", "crm:secret@tcp(mysql:3306)/crm?parseTime=true")
	t.Setenv("PORTAL_ACCESS_DISABLE_WORKER_ID", "worker-a")
	t.Setenv("PORTAL_ACCESS_DISABLE_PORTAL_URL", "https://portal.example/customer-portal/internal/accounts/disable")
	t.Setenv("PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_ID", "crm-disable")
	t.Setenv("PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_SECRET", "secret-a")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL", "https://identity.example/api/v1/internal/application-roles/revoke")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_ID", "crm-role-revoke")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_SECRET", "secret-b")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_APPLICATION_CODE", "customer_portal")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_BASE_URL", "https://platform.example")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_ID", "crm-audit")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_SECRET", "secret-c")
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_ENVIRONMENT_CODE", "dev")
}

func TestLoadConfigRequiresExactLeastPrivilegeScopes(t *testing.T) {
	setValidConfig(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Portal.Scope != requiredPortalScope || cfg.Platform.Scope != requiredPlatformScope || cfg.Audit.ApplicationCode != "customer_and_opportunity" || cfg.BatchSize != 20 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_SCOPE", "application_role.assign")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("broader/wrong platform scope must fail closed")
	}
}

func TestLoadConfigRequiresDedicatedPlatformAuditPublisher(t *testing.T) {
	setValidConfig(t)
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_ID", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("audit publisher client must be configured")
	}
	setValidConfig(t)
	t.Setenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_APPLICATION_CODE", "customer_portal")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("worker audit must publish as customer_and_opportunity")
	}
}

func TestLoadConfigRequiresHTTPSAndConfiguredMTLSIdentity(t *testing.T) {
	setValidConfig(t)
	t.Setenv("PORTAL_ACCESS_DISABLE_PORTAL_URL", "http://portal.example/internal/accounts/disable")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("clear-text integration URL must fail closed")
	}
	setValidConfig(t)
	t.Setenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_REQUIRE_MTLS", "true")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("required mTLS without client identity must fail closed")
	}
}
