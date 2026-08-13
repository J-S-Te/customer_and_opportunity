package portalinvitecompensationworker

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

const requiredPortalScope = "portal.identity_mapping.provision"

// Worker 使用独立 CRM 数据库连接和最小权限的 CRM→Portal OAuth 客户端；浏览器凭据及 Portal→CRM 凭据不得复用。
type Config struct {
	MySQLDSN                string
	WorkerID                string
	PollInterval            time.Duration
	LeaseDuration           time.Duration
	BatchSize               int
	ReconciliationInterval  time.Duration
	ReconciliationBatchSize int
	Portal                  PortalConfig
	Platform                PlatformConfig
}

type PlatformConfig struct {
	RoleAssignURL, TokenURL, ClientID, ClientSecret, Scope, ApplicationCode string
	TLS                                                                     integrationhttp.TLSOptions
}

type PortalConfig struct {
	ProvisionURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	TLS          integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	requireMTLS, err := boolEnv("PORTAL_INVITE_COMPENSATION_PLATFORM_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, err
	}
	portalRequireMTLS, err := boolEnv("PORTAL_INVITE_COMPENSATION_PORTAL_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		MySQLDSN:                os.Getenv("PORTAL_INVITE_COMPENSATION_MYSQL_DSN"),
		WorkerID:                env("PORTAL_INVITE_COMPENSATION_WORKER_ID", hostname()),
		PollInterval:            durationEnv("PORTAL_INVITE_COMPENSATION_POLL_INTERVAL", 5*time.Second),
		LeaseDuration:           durationEnv("PORTAL_INVITE_COMPENSATION_LEASE_DURATION", time.Minute),
		BatchSize:               intEnv("PORTAL_INVITE_COMPENSATION_BATCH_SIZE", 20),
		ReconciliationInterval:  durationEnv("PORTAL_IDENTITY_RECONCILIATION_INTERVAL", 5*time.Minute),
		ReconciliationBatchSize: intEnv("PORTAL_IDENTITY_RECONCILIATION_BATCH_SIZE", 100),
		Portal: PortalConfig{
			ProvisionURL: os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL"),
			TokenURL:     os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL"),
			ClientID:     os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_ID"),
			ClientSecret: os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_SECRET"),
			Scope:        env("PORTAL_INVITE_COMPENSATION_PORTAL_SCOPE", requiredPortalScope),
			TLS: integrationhttp.TLSOptions{
				RootCAFile: os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_TLS_CLIENT_CERT_FILE"),
				ClientKeyFile: os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_INVITE_COMPENSATION_PORTAL_TLS_SERVER_NAME"), RequireMTLS: portalRequireMTLS,
			},
		},
		Platform: PlatformConfig{
			RoleAssignURL:   os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_ROLE_ASSIGN_URL"),
			TokenURL:        os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TOKEN_URL"),
			ClientID:        os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_ID"),
			ClientSecret:    os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_SECRET"),
			Scope:           env("PORTAL_INVITE_COMPENSATION_PLATFORM_SCOPE", "application_role.assign"),
			ApplicationCode: os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_APPLICATION_CODE"),
			TLS: integrationhttp.TLSOptions{
				RootCAFile: os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TLS_CLIENT_CERT_FILE"),
				ClientKeyFile: os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_INVITE_COMPENSATION_PLATFORM_TLS_SERVER_NAME"),
				RequireMTLS: requireMTLS,
			},
		},
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	for key, value := range map[string]string{
		"PORTAL_INVITE_COMPENSATION_MYSQL_DSN":                 c.MySQLDSN,
		"PORTAL_INVITE_COMPENSATION_WORKER_ID":                 c.WorkerID,
		"PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL":      c.Portal.ProvisionURL,
		"PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL":          c.Portal.TokenURL,
		"PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_ID":          c.Portal.ClientID,
		"PORTAL_INVITE_COMPENSATION_PORTAL_CLIENT_SECRET":      c.Portal.ClientSecret,
		"PORTAL_INVITE_COMPENSATION_PLATFORM_ROLE_ASSIGN_URL":  c.Platform.RoleAssignURL,
		"PORTAL_INVITE_COMPENSATION_PLATFORM_TOKEN_URL":        c.Platform.TokenURL,
		"PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_ID":        c.Platform.ClientID,
		"PORTAL_INVITE_COMPENSATION_PLATFORM_CLIENT_SECRET":    c.Platform.ClientSecret,
		"PORTAL_INVITE_COMPENSATION_PLATFORM_APPLICATION_CODE": c.Platform.ApplicationCode,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if len(c.WorkerID) > 128 || c.PollInterval <= 0 || c.LeaseDuration < 15*time.Second || c.BatchSize < 1 || c.BatchSize > 100 ||
		c.ReconciliationInterval <= 0 || c.ReconciliationBatchSize < 1 || c.ReconciliationBatchSize > 100 {
		return fmt.Errorf("invalid Portal invite compensation scheduling configuration")
	}
	if strings.TrimSpace(c.Portal.Scope) != requiredPortalScope {
		return fmt.Errorf("PORTAL_INVITE_COMPENSATION_PORTAL_SCOPE must be %s", requiredPortalScope)
	}
	if strings.TrimSpace(c.Platform.Scope) != "application_role.assign" {
		return fmt.Errorf("PORTAL_INVITE_COMPENSATION_PLATFORM_SCOPE must be application_role.assign")
	}
	for key, raw := range map[string]string{
		"PORTAL_INVITE_COMPENSATION_PORTAL_PROVISION_URL": c.Portal.ProvisionURL,
		"PORTAL_INVITE_COMPENSATION_PORTAL_TOKEN_URL":     c.Portal.TokenURL,
	} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
		}
	}
	if err := c.Portal.TLS.ValidateEndpoints(c.Portal.TokenURL, c.Portal.ProvisionURL); err != nil {
		return fmt.Errorf("Portal invite compensation Portal TLS: %w", err)
	}
	if err := c.Platform.TLS.ValidateEndpoints(c.Platform.TokenURL, c.Platform.RoleAssignURL); err != nil {
		return fmt.Errorf("Portal invite compensation platform TLS: %w", err)
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func hostname() string {
	value, _ := os.Hostname()
	return value
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return parsed
}

func intEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
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
