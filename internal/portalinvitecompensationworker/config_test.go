package portalinvitecompensationworker

import (
	"testing"
	"time"
)

func TestLoadConfigRequiresDedicatedPortalScopeAndCredentials(t *testing.T) {
	t.Setenv("PORTAL_INVITE_COMPENSATION_MYSQL_DSN", "crm:secret@tcp(mysql:3306)/crm?parseTime=true")
	t.Setenv("PORTAL_INVITE_COMPENSATION_WORKER_ID", "worker-a")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL", "https://portal.example/customer-portal/internal/accounts/provision")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_ID", "crm-compensation")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_SCOPE", requiredPortalScope)
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_ROLE_ASSIGN_URL", "https://identity.example/api/v1/integrations/external-users/roles")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_ID", "crm-role-compensation")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_SCOPE", "application_role.assign")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_APPLICATION_CODE", "customer_portal")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Portal.Scope != requiredPortalScope || config.BatchSize != 20 || config.ReconciliationInterval != 5*time.Minute || config.ReconciliationBatchSize != 100 {
		t.Fatalf("unexpected config: %#v", config)
	}

	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_SCOPE", "portal.invite.verify")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("wrong machine scope must fail closed")
	}
}

func TestLoadConfigRejectsInvalidReconciliationScheduling(t *testing.T) {
	t.Setenv("PORTAL_INVITE_COMPENSATION_MYSQL_DSN", "crm:secret@tcp(mysql:3306)/crm?parseTime=true")
	t.Setenv("PORTAL_INVITE_COMPENSATION_WORKER_ID", "worker-a")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL", "https://portal.example/customer-portal/internal/accounts/provision")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_ID", "crm-compensation")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_ROLE_ASSIGN_URL", "https://identity.example/api/v1/internal/application-roles")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_ID", "crm-role-compensation")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_APPLICATION_CODE", "customer_portal")
	t.Setenv("PORTAL_IDENTITY_RECONCILIATION_INTERVAL", "0s")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("zero reconciliation interval was accepted")
	}
}

func TestLoadConfigPortalMTLSRequiresClientIdentity(t *testing.T) {
	t.Setenv("PORTAL_INVITE_COMPENSATION_MYSQL_DSN", "crm:secret@tcp(mysql:3306)/crm?parseTime=true")
	t.Setenv("PORTAL_INVITE_COMPENSATION_WORKER_ID", "worker-a")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL", "https://portal.example/customer-portal/internal/accounts/provision")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_ID", "crm-compensation")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PORTAL_TLS_REQUIRE_MTLS", "true")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_ROLE_ASSIGN_URL", "https://identity.example/api/v1/internal/application-roles")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TOKEN_URL", "https://identity.example/oauth2/token")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_ID", "crm-role-compensation")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_SECRET", "secret")
	t.Setenv("PORTAL_INVITE_COMPENSATION_PLATFORM_APPLICATION_CODE", "customer_portal")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("required Portal mTLS without client identity was accepted")
	}
}
