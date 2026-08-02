package bootstrap

import (
	"encoding/base64"
	"strings"
	"testing"
)

func setBaseConfig(t *testing.T) {
	t.Helper()
	t.Setenv("MYSQL_DSN", "crm:test@tcp(localhost:3306)/crm")
	t.Setenv("SENSITIVE_ENCRYPTION_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("e", 32))))
	t.Setenv("SENSITIVE_HMAC_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 32))))
}

func setPortalDisableConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PORTAL_DISABLE_URL", "https://portal.example/customer-portal/internal/accounts/disable")
	t.Setenv("PORTAL_DISABLE_CLIENT_ID", "crm-portal-disable")
	t.Setenv("PORTAL_DISABLE_CLIENT_SECRET", "secret-disable")
}

func setPlatformRoleRevokeConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PLATFORM_APPLICATION_ROLE_REVOKE_URL", "https://identity.example/api/v1/internal/application-roles/revoke")
	t.Setenv("PLATFORM_ROLE_REVOKE_CLIENT_ID", "crm-role-revoke")
	t.Setenv("PLATFORM_ROLE_REVOKE_CLIENT_SECRET", "secret-c")
}

func TestLoadConfigDevelopmentModeDoesNotRequireOIDC(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	config, err := LoadConfig()
	if err != nil || !config.DevelopmentAuth {
		t.Fatalf("LoadConfig() = %+v, %v", config, err)
	}
}

func TestLoadConfigPresaleWorkerHeartbeatFreshnessWindow(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	config, err := LoadConfig()
	if err != nil || config.PresaleWorkerHeartbeatMaxAge.String() != "15s" {
		t.Fatalf("default config=%+v err=%v", config, err)
	}
	t.Setenv("PRESALE_WORKER_HEARTBEAT_MAX_AGE", "2s")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "between 5s and 5m") {
		t.Fatalf("short heartbeat max age error=%v", err)
	}
}

func TestLoadConfigProductionRequiresCompleteOIDC(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "false")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestLoadConfigProductionOIDC(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "false")
	t.Setenv("OIDC_ISSUER", "https://identity.example.com")
	t.Setenv("OIDC_CLIENT_ID", "crm")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_REDIRECT_URI", "https://crm.example.com/customer-opportunity/auth/callback")
	t.Setenv("OIDC_TENANT_ID", "tenant-1")
	t.Setenv("OIDC_ROLE_CONFIG_HASH", "hash-1")
	t.Setenv("MACHINE_TOKEN_ISSUER", "basic-platform")
	t.Setenv("MACHINE_TOKEN_AUDIENCE", "basic-platform-application")
	t.Setenv("MACHINE_TOKEN_PUBLIC_KEY_PATH", "/run/secrets/basic-platform-application-jwt-public.pem")
	t.Setenv("APP_PUBLIC_ORIGIN", "https://crm.example.com")
	config, err := LoadConfig()
	if err != nil || config.DevelopmentAuth || config.OIDCSessionCookieName != "customer_opportunity_session" {
		t.Fatalf("LoadConfig() = %+v, %v", config, err)
	}
}

func TestLoadConfigLoopbackOIDCAllowsInsecureSessionCookieAndDerivedRoleHash(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "false")
	t.Setenv("OIDC_ISSUER", "http://localhost:8081")
	t.Setenv("OIDC_CLIENT_ID", "crm-dev-web")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_REDIRECT_URI", "http://localhost:8081/customer-opportunity/auth/callback")
	t.Setenv("OIDC_TENANT_ID", "tenant-1")
	t.Setenv("OIDC_ROLE_CONFIG_HASH", "hash-1")
	t.Setenv("OIDC_SESSION_COOKIE_SECURE", "false")
	t.Setenv("MACHINE_TOKEN_ISSUER", "basic-platform")
	t.Setenv("MACHINE_TOKEN_AUDIENCE", "basic-platform-application")
	t.Setenv("MACHINE_TOKEN_PUBLIC_KEY_PATH", "/run/secrets/basic-platform-application-jwt-public.pem")
	t.Setenv("APP_PUBLIC_ORIGIN", "http://127.0.0.1:8081")
	config, err := LoadConfig()
	if err != nil || config.OIDCSessionSecure || config.OIDCRoleConfigHash != "hash-1" {
		t.Fatalf("LoadConfig() = %+v, %v", config, err)
	}
}

func TestLoadConfigRejectsInsecureSessionCookieForNonLoopbackOrigin(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "false")
	t.Setenv("OIDC_ISSUER", "https://identity.example.com")
	t.Setenv("OIDC_CLIENT_ID", "crm")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_REDIRECT_URI", "http://crm.example.com/customer-opportunity/auth/callback")
	t.Setenv("OIDC_TENANT_ID", "tenant-1")
	t.Setenv("OIDC_ROLE_CONFIG_HASH", "hash-1")
	t.Setenv("OIDC_SESSION_COOKIE_SECURE", "false")
	t.Setenv("MACHINE_TOKEN_ISSUER", "basic-platform")
	t.Setenv("MACHINE_TOKEN_AUDIENCE", "basic-platform-application")
	t.Setenv("MACHINE_TOKEN_PUBLIC_KEY_PATH", "/run/secrets/basic-platform-application-jwt-public.pem")
	t.Setenv("APP_PUBLIC_ORIGIN", "http://crm.example.com")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestLoadConfigPortalInviteRequiresMachineProvisionContract(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("PORTAL_INVITE_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PORTAL_PROVISION") {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestLoadConfigPortalInviteMachineScopeIsFixed(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("PORTAL_INVITE_ENABLED", "true")
	t.Setenv("PORTAL_PROVISION_URL", "https://portal.example/customer-portal/internal/accounts/provision")
	t.Setenv("PORTAL_PROVISION_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_PROVISION_CLIENT_ID", "crm-portal")
	t.Setenv("PORTAL_PROVISION_CLIENT_SECRET", "secret")
	setPortalDisableConfig(t)
	t.Setenv("PORTAL_PROVISION_SCOPE", "portal.identity_mapping.provision report.callback.write")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PORTAL_PROVISION_SCOPE") {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestLoadConfigPortalProvisionMTLSRequiresClientIdentity(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("PORTAL_INVITE_ENABLED", "true")
	t.Setenv("PORTAL_PROVISION_URL", "https://portal.example/customer-portal/internal/accounts/provision")
	t.Setenv("PORTAL_PROVISION_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_PROVISION_CLIENT_ID", "crm-portal")
	t.Setenv("PORTAL_PROVISION_CLIENT_SECRET", "secret")
	setPortalDisableConfig(t)
	t.Setenv("PORTAL_PROVISION_TLS_REQUIRE_MTLS", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "Portal provision TLS") {
		t.Fatalf("missing Portal client identity error = %v", err)
	}
}

func TestLoadConfigPlatformExternalIdentityRequiresSeparateExactScopes(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("PLATFORM_EXTERNAL_IDENTITY_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PLATFORM_EXTERNAL") {
		t.Fatalf("incomplete platform external identity error = %v", err)
	}
	t.Setenv("PLATFORM_EXTERNAL_USER_PROVISION_URL", "https://identity.example/api/v1/internal/external-users")
	t.Setenv("PLATFORM_APPLICATION_ROLE_ASSIGN_URL", "https://identity.example/api/v1/internal/application-roles")
	t.Setenv("PLATFORM_MANAGEMENT_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PLATFORM_PORTAL_APPLICATION_CODE", "customer_portal")
	t.Setenv("PLATFORM_EXTERNAL_USER_CLIENT_ID", "crm-external-user")
	t.Setenv("PLATFORM_EXTERNAL_USER_CLIENT_SECRET", "secret-a")
	t.Setenv("PLATFORM_ROLE_ASSIGN_CLIENT_ID", "crm-role-assign")
	t.Setenv("PLATFORM_ROLE_ASSIGN_CLIENT_SECRET", "secret-b")
	setPlatformRoleRevokeConfig(t)
	t.Setenv("PLATFORM_EXTERNAL_USER_SCOPE", "external_user.provision user.write")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "scopes") {
		t.Fatalf("over-scoped platform external identity error = %v", err)
	}
	t.Setenv("PLATFORM_EXTERNAL_USER_SCOPE", "external_user.provision")
	config, err := LoadConfig()
	if err != nil || !config.PlatformExternalIdentityEnabled || config.PlatformRoleAssignScope != "application_role.assign" {
		t.Fatalf("LoadConfig() = %#v, %v", config, err)
	}
}

func TestLoadConfigApprovalTaskResolverIsOptionalAndExactlyScoped(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("APPROVAL_TASK_RESOLVER_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "APPROVAL_TASK") {
		t.Fatalf("incomplete approval task resolver error = %v", err)
	}
	t.Setenv("APPROVAL_TASK_URL", "https://approval.example/api/v1/internal/current-task")
	t.Setenv("APPROVAL_TASK_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("APPROVAL_TASK_CLIENT_ID", "crm-approval-reader")
	t.Setenv("APPROVAL_TASK_CLIENT_SECRET", "secret")
	t.Setenv("APPROVAL_TASK_SCOPE", "presale.approval.task.read approval.write")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "APPROVAL_TASK_SCOPE") {
		t.Fatalf("over-scoped approval task resolver error = %v", err)
	}
	t.Setenv("APPROVAL_TASK_SCOPE", "presale.approval.task.read")
	config, err := LoadConfig()
	if err != nil || !config.ApprovalTaskResolverEnabled {
		t.Fatalf("LoadConfig() = %#v, %v", config, err)
	}
}

func TestLoadConfigCatalogSynchronizationIsOptionalButCompleteWhenEnabled(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PLATFORM_AUTHORIZATION_CATALOG") {
		t.Fatalf("LoadConfig() incomplete catalog error = %v", err)
	}
	t.Setenv("PLATFORM_BASE_URL", "https://identity.example.com")
	t.Setenv("PLATFORM_AUTHORIZATION_CATALOG_APPLICATION_ID", "crm-app")
	t.Setenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_ID", "crm-publisher")
	t.Setenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET", "secret")
	config, err := LoadConfig()
	if err != nil || !config.CatalogSyncEnabled || config.CatalogApplicationID != "crm-app" {
		t.Fatalf("LoadConfig() = %+v, %v", config, err)
	}
}

func TestLoadConfigProjectHistoryIsOptionalButCompleteAndMinimumScopedWhenEnabled(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("PORTAL_PROJECT_HISTORY_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PORTAL_PROJECT_HISTORY") {
		t.Fatalf("incomplete project history error=%v", err)
	}
	t.Setenv("PORTAL_PROJECT_HISTORY_URL", "https://portal.example/customer-portal/internal/customers")
	t.Setenv("PORTAL_PROJECT_HISTORY_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_PROJECT_HISTORY_CLIENT_ID", "crm-project-history")
	t.Setenv("PORTAL_PROJECT_HISTORY_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_PROJECT_HISTORY_SCOPE", "portal.project_history.read portal.feedback.manage")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PORTAL_PROJECT_HISTORY_SCOPE") {
		t.Fatalf("over-scoped project history error=%v", err)
	}
	t.Setenv("PORTAL_PROJECT_HISTORY_SCOPE", "portal.project_history.read")
	config, err := LoadConfig()
	if err != nil || !config.PortalProjectHistoryEnabled {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}

func TestLoadConfigContractVerificationIsOptionalButCompleteAndExactlyScopedWhenEnabled(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	config, err := LoadConfig()
	if err != nil || config.ContractVerificationEnabled {
		t.Fatalf("default config=%#v err=%v", config, err)
	}
	t.Setenv("CONTRACT_VERIFICATION_ENABLED", "true")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "CONTRACT_SUMMARY") {
		t.Fatalf("incomplete contract verification error=%v", err)
	}
	t.Setenv("CONTRACT_SUMMARY_URL", "https://contract.example/internal/contract-summary")
	t.Setenv("CONTRACT_SUMMARY_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("CONTRACT_SUMMARY_CLIENT_ID", "crm-contract-summary")
	t.Setenv("CONTRACT_SUMMARY_CLIENT_SECRET", "secret")
	t.Setenv("CONTRACT_SUMMARY_SCOPE", "contract.summary.read contract.write")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "CONTRACT_SUMMARY_SCOPE") {
		t.Fatalf("over-scoped contract verification error=%v", err)
	}
	t.Setenv("CONTRACT_SUMMARY_SCOPE", "contract.summary.read")
	config, err = LoadConfig()
	if err != nil || !config.ContractVerificationEnabled {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}

func TestLoadConfigOwnerDirectoryIsOptionalButCompleteAndExactlyScopedWhenEnabled(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	config, err := LoadConfig()
	if err != nil || config.OwnerDirectoryEnabled {
		t.Fatalf("default config=%#v err=%v", config, err)
	}
	t.Setenv("OWNER_DIRECTORY_ENABLED", "true")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "PLATFORM_OWNER_DIRECTORY") {
		t.Fatalf("incomplete owner directory error=%v", err)
	}
	t.Setenv("PLATFORM_OWNER_DIRECTORY_URL", "https://identity.example/api/v1/internal/owner-directory")
	t.Setenv("PLATFORM_MANAGEMENT_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PLATFORM_OWNER_DIRECTORY_CLIENT_ID", "crm-owner-directory")
	t.Setenv("PLATFORM_OWNER_DIRECTORY_CLIENT_SECRET", "secret")
	t.Setenv("PLATFORM_OWNER_DIRECTORY_SCOPE", "owner_directory.read owner_directory.write")
	if _, err = LoadConfig(); err == nil || !strings.Contains(err.Error(), "PLATFORM_OWNER_DIRECTORY_SCOPE") {
		t.Fatalf("over-scoped owner directory error=%v", err)
	}
	t.Setenv("PLATFORM_OWNER_DIRECTORY_SCOPE", "owner_directory.read")
	config, err = LoadConfig()
	if err != nil || !config.OwnerDirectoryEnabled {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}

func TestLoadConfigQBStatusAndLaunchAreOptionalAndFailClosed(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	t.Setenv("QB_STATUS_QUERY_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "QB_STATUS") {
		t.Fatalf("incomplete status query error=%v", err)
	}
	t.Setenv("QB_STATUS_URL", "https://qb.example/internal/by-opportunity")
	t.Setenv("QB_STATUS_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("QB_STATUS_CLIENT_ID", "crm-qb-reader")
	t.Setenv("QB_STATUS_CLIENT_SECRET", "secret")
	t.Setenv("QB_STATUS_SCOPE", "opportunity.status.read opportunity.status.write")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "QB_STATUS_SCOPE") {
		t.Fatalf("over-scoped status query error=%v", err)
	}
	t.Setenv("QB_STATUS_SCOPE", "opportunity.status.read")
	t.Setenv("QB_LAUNCH_ENABLED", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "QB_LAUNCH_SIGNING_KEY_BASE64") {
		t.Fatalf("incomplete launch error=%v", err)
	}
	t.Setenv("QB_QUOTATION_PUBLIC_URL", "https://qb.example/quotation")
	t.Setenv("QB_BID_PUBLIC_URL", "https://qb.example/bid")
	t.Setenv("QB_LAUNCH_SIGNING_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("q", 32))))
	config, err := LoadConfig()
	if err != nil || !config.QBStatusQueryEnabled || !config.QBLaunchEnabled {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}
