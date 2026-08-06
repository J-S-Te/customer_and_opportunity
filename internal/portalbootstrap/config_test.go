package portalbootstrap

import (
	"strings"
	"testing"
	"time"
)

func validPortalConfig() Config {
	return Config{
		MySQLDSN: "portal:secret@tcp(mysql:3306)/portal", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example",
		TenantID: "tenant-a", RoleConfigHash: "sha256:catalog", OIDCIssuer: "https://identity.example",
		OIDCClientID: "portal-browser", OIDCClientSecret: "browser-secret", OIDCRedirectURI: "https://portal.example/customer-portal/auth/callback", OIDCScopes: []string{"openid", "profile"},
		SessionCookieName: "portal_session", SessionCookieSecure: true, SessionTTL: 15 * time.Minute,
		AccountSecurityCenterURL: "https://identity.example/account/security",
		MachineTokenIssuer:       "basic-platform", MachineTokenAudience: "basic-platform-application",
		MachineTokenPublicKeyPath: "/run/secrets/basic-platform-application-jwt-public.pem",
		CRMProvisionClientSubject: "crm-portal-provision", CRMDisableClientSubject: "crm-portal-disable",
		ProjectHistoryStaleAfter: 10 * time.Minute,
		CRMInviteBaseURL:         "https://crm.example/customer-opportunity/api/v1", CRMInviteTokenURL: "https://identity.example/oauth2/token",
		CRMInviteClientID: "portal-crm-invite", CRMInviteClientSecret: "machine-secret", CRMInviteScope: "portal.invite.verify",
		EncryptionKey: []byte(strings.Repeat("e", 32)), ReportIngestDescriptorKey: []byte(strings.Repeat("r", 32)), HMACKey: []byte(strings.Repeat("h", 32)),
		PlatformBaseURL: "https://identity.example", PlatformApplicationCode: "customer_portal", PlatformEnvironmentCode: "test",
		PlatformAuditClientID: "portal-audit", PlatformAuditClientSecret: "audit-secret", PlatformAuditWorkerID: "portal-api-audit",
		PlatformAuditPollInterval: time.Second, PlatformAuditBatchSize: 100,
	}
}

func TestPortalProjectHistoryStalenessThresholdIsPositive(t *testing.T) {
	config := validPortalConfig()
	config.ProjectHistoryStaleAfter = -time.Second
	if err := config.validate(); err == nil || !strings.Contains(err.Error(), "PORTAL_PROJECT_HISTORY_STALE_AFTER") {
		t.Fatalf("validate() error=%v", err)
	}
}

func TestPortalReportIngestDescriptorKeyIsDedicated(t *testing.T) {
	config := validPortalConfig()
	config.ReportIngestDescriptorKey = nil
	if err := config.validate(); err == nil {
		t.Fatal("missing report ingest descriptor key was accepted")
	}
	config = validPortalConfig()
	config.ReportIngestDescriptorKey = append([]byte(nil), config.EncryptionKey...)
	if err := config.validate(); err == nil {
		t.Fatal("reused general encryption key was accepted")
	}
}

func TestPortalConfigRequiresDedicatedCRMInviteMachineContract(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"client id": func(value *Config) { value.CRMInviteClientID = "" },
		"secret":    func(value *Config) { value.CRMInviteClientSecret = "" },
		"scope":     func(value *Config) { value.CRMInviteScope = "portal.invite.verify customer.summary.read" },
		"token URL": func(value *Config) { value.CRMInviteTokenURL = "https://secret@identity.example/oauth2/token" },
	} {
		t.Run(name, func(t *testing.T) {
			config := validPortalConfig()
			mutate(&config)
			if err := config.validate(); err == nil {
				t.Fatal("expected invalid CRM invitation configuration")
			}
		})
	}
	if err := validPortalConfig().validate(); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
}

func TestPortalConfigRequiresDeploymentMachineAudience(t *testing.T) {
	config := validPortalConfig()
	config.MachineTokenAudience = ""
	if err := config.validate(); err == nil || !strings.Contains(err.Error(), "PORTAL_MACHINE_TOKEN_AUDIENCE") {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestPortalConfigRequiresApplicationJWTIssuerAndPublicKey(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"issuer":     func(value *Config) { value.MachineTokenIssuer = "" },
		"public key": func(value *Config) { value.MachineTokenPublicKeyPath = "" },
	} {
		t.Run(name, func(t *testing.T) {
			config := validPortalConfig()
			mutate(&config)
			if err := config.validate(); err == nil {
				t.Fatal("expected missing application JWT configuration to be rejected")
			}
		})
	}
}

func TestPortalConfigRequiresDedicatedCRMAccountClientSubjects(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"missing provision subject": func(value *Config) { value.CRMProvisionClientSubject = "" },
		"missing disable subject":   func(value *Config) { value.CRMDisableClientSubject = "" },
		"untrimmed subject":         func(value *Config) { value.CRMDisableClientSubject = " crm-portal-disable" },
		"oversized subject":         func(value *Config) { value.CRMDisableClientSubject = strings.Repeat("x", 57) },
	} {
		t.Run(name, func(t *testing.T) {
			config := validPortalConfig()
			mutate(&config)
			if err := config.validate(); err == nil {
				t.Fatal("expected invalid CRM account client subject configuration")
			}
		})
	}
}

func TestPortalConfigRequiresTrustedSecurityCenterURL(t *testing.T) {
	for _, value := range []string{"", "http://identity.example/account/security", "https://user:secret@identity.example/security", "https://identity.example/security?return_to=https://evil.example"} {
		config := validPortalConfig()
		config.AccountSecurityCenterURL = value
		if err := config.validate(); err == nil {
			t.Fatalf("untrusted security center URL accepted: %q", value)
		}
	}
}

func TestPortalConfigAllowsHTTPAccountSecurityCenterOnlyOnLoopback(t *testing.T) {
	for _, value := range []string{"http://localhost:8081/settings/security", "http://127.0.0.1:8081/settings/security", "http://[::1]:8081/settings/security"} {
		config := validPortalConfig()
		config.AccountSecurityCenterURL = value
		if err := config.validate(); err != nil {
			t.Fatalf("loopback security center %q rejected: %v", value, err)
		}
	}
	config := validPortalConfig()
	config.AccountSecurityCenterURL = "http://192.168.3.11:8081/settings/security"
	if err := config.validate(); err == nil {
		t.Fatal("non-loopback HTTP account security center must be rejected")
	}
}

func TestPortalConfigAllowsHTTPAccountSecurityCenterWithExplicitTestToggle(t *testing.T) {
	config := validPortalConfig()
	config.AccountSecurityCenterURL = "http://192.168.3.11:8081/settings/security"
	config.AllowInsecureHTTPSession = true
	if err := config.validate(); err != nil {
		t.Fatalf("explicit test toggle rejected non-loopback HTTP security center: %v", err)
	}
}

func TestPortalConfigCatalogSynchronizationIsOptionalButCompleteWhenEnabled(t *testing.T) {
	config := validPortalConfig()
	config.CatalogSyncEnabled = true
	config.CatalogApplicationID = ""
	config.CatalogClientID = ""
	config.CatalogClientSecret = ""
	if err := config.validate(); err == nil || !strings.Contains(err.Error(), "PORTAL_AUTHORIZATION_CATALOG") {
		t.Fatalf("incomplete catalog configuration error = %v", err)
	}
	config.PlatformBaseURL = "https://identity.example"
	config.CatalogApplicationID = "portal-app"
	config.CatalogClientID = "portal-publisher"
	config.CatalogClientSecret = "secret"
	if err := config.validate(); err != nil {
		t.Fatalf("valid catalog configuration rejected: %v", err)
	}
	config.PlatformBaseURL = "https://user:secret@identity.example"
	if err := config.validate(); err == nil {
		t.Fatal("catalog platform URL containing credentials was accepted")
	}
}
