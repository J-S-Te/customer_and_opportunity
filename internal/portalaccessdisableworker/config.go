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
	cfg := Config{
		MySQLDSN: os.Getenv("PORTAL_ACCESS_DISABLE_MYSQL_DSN"), WorkerID: env("PORTAL_ACCESS_DISABLE_WORKER_ID", hostname()),
		PollInterval: durationEnv("PORTAL_ACCESS_DISABLE_POLL_INTERVAL", 5*time.Second), LeaseDuration: durationEnv("PORTAL_ACCESS_DISABLE_LEASE_DURATION", time.Minute),
		BatchSize: intEnv("PORTAL_ACCESS_DISABLE_BATCH_SIZE", 20),
		Portal: PortalConfig{
			DisableURL: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_URL"), TokenURL: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL"),
			ClientID: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_SECRET"),
			Scope: env("PORTAL_ACCESS_DISABLE_PORTAL_SCOPE", requiredPortalScope),
			TLS:   integrationhttp.TLSOptions{RootCAFile: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_ACCESS_DISABLE_PORTAL_TLS_SERVER_NAME"), RequireMTLS: portalMTLS},
		},
		Platform: PlatformConfig{
			RoleRevokeURL: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL"), TokenURL: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL"),
			ClientID: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_SECRET"),
			Scope: env("PORTAL_ACCESS_DISABLE_PLATFORM_SCOPE", requiredPlatformScope), ApplicationCode: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_APPLICATION_CODE"),
			TLS: integrationhttp.TLSOptions{RootCAFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_ACCESS_DISABLE_PLATFORM_TLS_SERVER_NAME"), RequireMTLS: platformMTLS},
		},
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	for key, value := range map[string]string{
		"PORTAL_ACCESS_DISABLE_MYSQL_DSN": c.MySQLDSN, "PORTAL_ACCESS_DISABLE_WORKER_ID": c.WorkerID,
		"PORTAL_ACCESS_DISABLE_PORTAL_URL": c.Portal.DisableURL, "PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL": c.Portal.TokenURL,
		"PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_ID": c.Portal.ClientID, "PORTAL_ACCESS_DISABLE_PORTAL_CLIENT_SECRET": c.Portal.ClientSecret,
		"PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL": c.Platform.RoleRevokeURL, "PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL": c.Platform.TokenURL,
		"PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_ID": c.Platform.ClientID, "PORTAL_ACCESS_DISABLE_PLATFORM_CLIENT_SECRET": c.Platform.ClientSecret,
		"PORTAL_ACCESS_DISABLE_PLATFORM_APPLICATION_CODE": c.Platform.ApplicationCode,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s is required and must not contain surrounding whitespace", key)
		}
	}
	if len(c.WorkerID) > 128 || c.PollInterval <= 0 || c.LeaseDuration < 30*time.Second || c.BatchSize < 1 || c.BatchSize > 100 {
		return fmt.Errorf("invalid Portal access disable worker scheduling configuration")
	}
	if c.Portal.Scope != requiredPortalScope {
		return fmt.Errorf("PORTAL_ACCESS_DISABLE_PORTAL_SCOPE must be %s", requiredPortalScope)
	}
	if c.Platform.Scope != requiredPlatformScope {
		return fmt.Errorf("PORTAL_ACCESS_DISABLE_PLATFORM_SCOPE must be %s", requiredPlatformScope)
	}
	for key, raw := range map[string]string{
		"PORTAL_ACCESS_DISABLE_PORTAL_URL": c.Portal.DisableURL, "PORTAL_ACCESS_DISABLE_PORTAL_TOKEN_URL": c.Portal.TokenURL,
		"PORTAL_ACCESS_DISABLE_PLATFORM_ROLE_REVOKE_URL": c.Platform.RoleRevokeURL, "PORTAL_ACCESS_DISABLE_PLATFORM_TOKEN_URL": c.Platform.TokenURL,
	} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s must be an HTTPS URL without credentials, query or fragment", key)
		}
	}
	if err := c.Portal.TLS.ValidateEndpoints(c.Portal.TokenURL, c.Portal.DisableURL); err != nil {
		return fmt.Errorf("Portal access disable Portal TLS: %w", err)
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
