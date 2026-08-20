package portalaccessdisableworker

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

const (
	requiredPortalScope   = "portal.identity_mapping.disable"
	requiredPlatformScope = "application_role.revoke"
)

type Config struct {
	MySQLDSN      string
	WorkerID      string
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
	Portal        PortalConfig
	Platform      PlatformConfig
	Audit         AuditConfig
	// PlatformOnly 进入 Phase 5 单写：跳过门户禁用调用，平台绑定禁用成为唯一远程收敛点。
	PlatformOnly bool
	Binding      BindingConfig
}

// AuditConfig 使用独立的 audit.ingest 机器客户端把 Worker 产生的业务审计
// 通过 CRM 的持久化 Outbox 投递到基础平台。不得复用角色撤销客户端，避免扩大其权限。
type AuditConfig struct {
	BaseURL, ClientID, ClientSecret, ApplicationCode, EnvironmentCode, WorkerID string
	PollInterval                                                                time.Duration
	BatchSize                                                                   int
}

// BindingConfig 是平台客户绑定禁用收敛的机器凭据（portal_mapping_disable scope）。
type BindingConfig struct {
	BaseURL, ClientID, ClientSecret, Scope string
	TLS                                    integrationhttp.TLSOptions
}

type PortalConfig struct {
	DisableURL, TokenURL, ClientID, ClientSecret, Scope string
	TLS                                                 integrationhttp.TLSOptions
}

type PlatformConfig struct {
	RoleRevokeURL, TokenURL, ClientID, ClientSecret, Scope, ApplicationCode string
	TLS                                                                     integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	portalMTLS, err := boolEnv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, err
	}
	platformMTLS, err := boolEnv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, err
	}
	platformOnly, err := boolEnv("PORTAL_ACCESS_DISABLE_PLATFORM_ONLY", false)
	if err != nil {
		return Config{}, err
	}
	auditPollInterval := durationEnv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_POLL_INTERVAL", time.Second)
	cfg := Config{
		PlatformOnly: platformOnly,
		MySQLDSN:     os.Getenv("PORTAL_ACCESS_DISABLE_MYSQL_DSN"), WorkerID: env("PORTAL_ACCESS_DISABLE_WORKER_ID", hostname()),
		PollInterval: durationEnv("PORTAL_ACCESS_DISABLE_POLL_INTERVAL", 5*time.Second), LeaseDuration: durationEnv("PORTAL_ACCESS_DISABLE_LEASE_DURATION", time.Minute),
		BatchSize: intEnv("PORTAL_ACCESS_DISABLE_BATCH_SIZE", 20),
		Portal: PortalConfig{
			DisableURL: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_URL"), TokenURL: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL"),
			ClientID: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_SECRET"),
			Scope: env("PORTAL_ACCESS_DISABLE_PORTAL_SCOPE", requiredPortalScope),
			TLS:   integrationhttp.TLSOptions{RootCAFile: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_SERVER_NAME"), RequireMTLS: portalMTLS},
		},
		Binding: BindingConfig{
			BaseURL:  os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_BASE_URL"),
			ClientID: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_CLIENT_SECRET"),
			Scope: env("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_SCOPE", "portal_mapping_disable"),
			TLS:   integrationhttp.TLSOptions{RootCAFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_SERVER_NAME"), RequireMTLS: platformMTLS},
		},
		Platform: PlatformConfig{
			RoleRevokeURL: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL"), TokenURL: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL"),
			ClientID: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_SECRET"),
			Scope: env("PORTAL_ACCESS_DISABLE_PLATFORM_SCOPE", requiredPlatformScope), ApplicationCode: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_APPLICATION_CODE"),
			TLS: integrationhttp.TLSOptions{RootCAFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_SERVER_NAME"), RequireMTLS: platformMTLS},
		},
		Audit: AuditConfig{
			BaseURL:  os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_BASE_URL"),
			ClientID: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_SECRET"),
			ApplicationCode: env("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_APPLICATION_CODE", "customer_and_opportunity"),
			EnvironmentCode: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_ENVIRONMENT_CODE"),
			WorkerID:        env("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_WORKER_ID", "portal-access-disable-audit"),
			PollInterval:    auditPollInterval, BatchSize: intEnv("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_BATCH_SIZE", 100),
		},
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	required := map[string]string{
		"PORTAL_ACCESS_DISABLE_MYSQL_DSN": c.MySQLDSN, "PORTAL_ACCESS_DISABLE_WORKER_ID": c.WorkerID,
		"PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL": c.Platform.RoleRevokeURL, "PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL": c.Platform.TokenURL,
		"PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_ID": c.Platform.ClientID, "PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_SECRET": c.Platform.ClientSecret,
		"PORTAL_ACCESS_DISABLE_PLATFORM_APPLICATION_CODE":       c.Platform.ApplicationCode,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_BASE_URL":         c.Audit.BaseURL,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_ID":        c.Audit.ClientID,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_CLIENT_SECRET":    c.Audit.ClientSecret,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_APPLICATION_CODE": c.Audit.ApplicationCode,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_ENVIRONMENT_CODE": c.Audit.EnvironmentCode,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_WORKER_ID":        c.Audit.WorkerID,
	}
	if !c.PlatformOnly {
		required["PORTAL_ACCESS_DISABLE_PORTAL_URL"] = c.Portal.DisableURL
		required["PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL"] = c.Portal.TokenURL
		required["PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_ID"] = c.Portal.ClientID
		required["PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_SECRET"] = c.Portal.ClientSecret
	} else {
		required["PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_BASE_URL"] = c.Binding.BaseURL
		required["PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_CLIENT_ID"] = c.Binding.ClientID
		required["PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_CLIENT_SECRET"] = c.Binding.ClientSecret
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s is required and must not contain surrounding whitespace", key)
		}
	}
	if len(c.WorkerID) > 128 || c.PollInterval <= 0 || c.LeaseDuration < 30*time.Second || c.BatchSize < 1 || c.BatchSize > 100 {
		return fmt.Errorf("invalid Portal access disable worker scheduling configuration")
	}
	if !c.PlatformOnly && c.Portal.Scope != requiredPortalScope {
		return fmt.Errorf("PORTAL_ACCESS_DISABLE_PORTAL_SCOPE must be %s", requiredPortalScope)
	}
	if c.Platform.Scope != requiredPlatformScope {
		return fmt.Errorf("PORTAL_ACCESS_DISABLE_PLATFORM_SCOPE must be %s", requiredPlatformScope)
	}
	if c.Audit.ApplicationCode != "customer_and_opportunity" {
		return fmt.Errorf("PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_APPLICATION_CODE must be customer_and_opportunity")
	}
	if c.Audit.PollInterval < 100*time.Millisecond || c.Audit.PollInterval > time.Minute || c.Audit.BatchSize < 1 || c.Audit.BatchSize > 100 {
		return fmt.Errorf("invalid Portal access disable audit dispatcher configuration")
	}
	if c.PlatformOnly && c.Binding.Scope != "portal_mapping_disable" {
		return fmt.Errorf("PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_SCOPE must be portal_mapping_disable")
	}
	endpoints := map[string]string{
		"PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL": c.Platform.RoleRevokeURL, "PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL": c.Platform.TokenURL,
		"PORTAL_ACCESS_DISABLE_PLATFORM_AUDIT_BASE_URL": c.Audit.BaseURL,
	}
	if !c.PlatformOnly {
		endpoints["PORTAL_ACCESS_DISABLE_PORTAL_URL"] = c.Portal.DisableURL
		endpoints["PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL"] = c.Portal.TokenURL
	} else {
		endpoints["PORTAL_ACCESS_DISABLE_PLATFORM_BINDING_BASE_URL"] = c.Binding.BaseURL
	}
	for key, raw := range endpoints {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s must be an HTTPS URL without credentials, query or fragment", key)
		}
	}
	if !c.PlatformOnly {
		if err := c.Portal.TLS.ValidateEndpoints(c.Portal.TokenURL, c.Portal.DisableURL); err != nil {
			return fmt.Errorf("Portal access disable Portal TLS: %w", err)
		}
	} else if err := c.Binding.TLS.ValidateEndpoints(c.Platform.TokenURL, c.Binding.BaseURL); err != nil {
		return fmt.Errorf("Portal access disable binding TLS: %w", err)
	}
	if err := c.Platform.TLS.ValidateEndpoints(c.Platform.TokenURL, c.Platform.RoleRevokeURL); err != nil {
		return fmt.Errorf("Portal access disable platform TLS: %w", err)
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func hostname() string { value, _ := os.Hostname(); return value }

func durationEnv(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return -1
		}
		return parsed
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return -1
		}
		return parsed
	}
	return fallback
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return parsed, nil
}
