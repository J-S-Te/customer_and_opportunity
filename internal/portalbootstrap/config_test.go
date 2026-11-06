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
		OIDCIdentityProviderHint: "basic-platform",
		SessionCookieName:        "portal_session", SessionCookieSecure: true, SessionTTL: 15 * time.Minute,
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

func TestPortalPDFConfigMustBePaired(t *testing.T) {
	config := validPortalConfig()
	config.PDFConfigRoot = "/tmp/portal-pdf"
	if err := config.validate(); err == nil {
		t.Fatal("unpaired PDF config accepted")
	}
	config.CJKFontPath = "/opt/fonts/approved-cjk.ttf"
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	config.PDFConfigRoot = ""
	if err := config.validate(); err == nil {
		t.Fatal("font without config root accepted")
	}
}

func TestPortalConfigRejectsUntrimmedIdentityProviderHint(t *testing.T) {
	config := validPortalConfig()
	config.OIDCIdentityProviderHint = " basic-platform"
	if err := config.validate(); err == nil || !strings.Contains(err.Error(), "PORTAL_OIDC_IDP_HINT") {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestPortalOIDCIdentityProviderHintDefaultsAndCanBeOverridden(t *testing.T) {
	t.Setenv("PORTAL_OIDC_IDP_HINT", "")
	if got := portalOIDCIdentityProviderHint(); got != "basic-platform-customer" {
		t.Fatalf("default identity provider hint = %q", got)
	}
	t.Setenv("PORTAL_OIDC_IDP_HINT", "corporate-broker")
	if got := portalOIDCIdentityProviderHint(); got != "corporate-broker" {
		t.Fatalf("configured identity provider hint = %q", got)
	}
}

func TestPortalProjectHistoryStalenessThresholdIsPositive(t *testing.T) {
	config := validPortalConfig()
	config.ProjectHistoryStaleAfter = -time.Second
	if err := config.validate(); err == nil || !strings.Contains(err.Error(), "PORTAL_PROJECT_HISTORY_STALE_AFTER") {
		t.Fatalf("validate() error=%v", err)
	}
}

func TestPortalConfigAcceptsKeycloakRealmIssuer(t *testing.T) {
	config := validPortalConfig()
	config.OIDCIssuer = "https://sso.example.com/realms/basic-platform"
	if err := config.validate(); err != nil {
		t.Fatalf("Keycloak realm issuer rejected: %v", err)
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

// M-10：登录入口额度必须可配、有区间；0 表示取默认值而不是关闭限流，
// 因此"配置遗漏"不会静默变成不限流。
func TestPortalAuthRateLimitConfigurationIsBounded(t *testing.T) {
	if err := validPortalConfig().validate(); err != nil {
		t.Fatalf("zero-value rate limits rejected: %v", err)
	}
	config := validPortalConfig()
	config.LoginRateLimit = 20
	config.ActivateRateLimit = 20
	config.CallbackRateLimit = 30
	config.RateLimitWindow = time.Minute
	if err := config.validate(); err != nil {
		t.Fatalf("valid rate limits rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"登录额度为负": func(value *Config) { value.LoginRateLimit = -1 },
		"激活额度过大": func(value *Config) { value.ActivateRateLimit = maxAuthRateLimit + 1 },
		"回调额度过大": func(value *Config) { value.CallbackRateLimit = maxAuthRateLimit + 1 },
		"窗口过短":   func(value *Config) { value.RateLimitWindow = time.Millisecond },
		"窗口过长":   func(value *Config) { value.RateLimitWindow = 2 * time.Hour },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validPortalConfig()
			mutate(&invalid)
			err := invalid.validate()
			if err == nil {
				t.Fatal("invalid rate limit configuration accepted")
			}
			if !strings.Contains(err.Error(), "RATE_LIMIT") {
				t.Fatalf("error must name the offending key: %v", err)
			}
		})
	}
}

func TestPortalAuthRateLimitEnvironmentOverrides(t *testing.T) {
	for name, want := range map[string]int{"3": 3, "0": 0} {
		t.Setenv("PORTAL_LOGIN_RATE_LIMIT", name)
		got, err := intValueOrDefault("PORTAL_LOGIN_RATE_LIMIT", defaultLoginRateLimit)
		if err != nil || got != want {
			t.Fatalf("override %q -> %d (%v)", name, got, err)
		}
	}
	t.Setenv("PORTAL_LOGIN_RATE_LIMIT", "not-a-number")
	if _, err := intValueOrDefault("PORTAL_LOGIN_RATE_LIMIT", defaultLoginRateLimit); err == nil {
		t.Fatal("malformed rate limit must fail fast instead of silently defaulting")
	}
}
