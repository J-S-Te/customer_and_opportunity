package portalprojectworker

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

const requiredProjectScope = "project.snapshot.read"

// 项目同步配置与浏览器 Portal 及其他机器集成隔离；其 OAuth 凭据只读项目快照，不能复用于报告提交。
type Config struct {
	MySQLDSN       string
	TenantID       string
	WorkerID       string
	PollInterval   time.Duration
	SyncInterval   time.Duration
	LeaseDuration  time.Duration
	RetryInterval  time.Duration
	PageSize       int
	TokenURL       string
	ClientID       string
	ClientSecret   string
	Scope          string
	SnapshotsURL   string
	TokenTimeout   time.Duration
	RequestTimeout time.Duration
	TLS            integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	requireMTLS, err := boolEnv("PORTAL_PROJECT_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		MySQLDSN: os.Getenv("PORTAL_PROJECT_MYSQL_DSN"), TenantID: os.Getenv("PORTAL_PROJECT_TENANT_ID"),
		WorkerID:      env("PORTAL_PROJECT_WORKER_ID", hostname()),
		PollInterval:  durationEnv("PORTAL_PROJECT_POLL_INTERVAL", 5*time.Second),
		SyncInterval:  durationEnv("PORTAL_PROJECT_SYNC_INTERVAL", 5*time.Minute),
		LeaseDuration: durationEnv("PORTAL_PROJECT_LEASE_DURATION", time.Minute),
		RetryInterval: durationEnv("PORTAL_PROJECT_RETRY_INTERVAL", time.Minute),
		PageSize:      intEnv("PORTAL_PROJECT_PAGE_SIZE", 100),
		TokenURL:      os.Getenv("PORTAL_PROJECT_TOKEN_URL"), ClientID: os.Getenv("PORTAL_PROJECT_CLIENT_ID"),
		ClientSecret: os.Getenv("PORTAL_PROJECT_CLIENT_SECRET"), Scope: env("PORTAL_PROJECT_SCOPE", requiredProjectScope),
		SnapshotsURL:   os.Getenv("PORTAL_PROJECT_SNAPSHOTS_URL"),
		TokenTimeout:   durationEnv("PORTAL_PROJECT_TOKEN_TIMEOUT", 5*time.Second),
		RequestTimeout: durationEnv("PORTAL_PROJECT_REQUEST_TIMEOUT", 10*time.Second),
		TLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("PORTAL_PROJECT_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_PROJECT_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("PORTAL_PROJECT_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_PROJECT_TLS_SERVER_NAME"),
			RequireMTLS: requireMTLS,
		},
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	for key, value := range map[string]string{
		"PORTAL_PROJECT_MYSQL_DSN": c.MySQLDSN, "PORTAL_PROJECT_TENANT_ID": c.TenantID,
		"PORTAL_PROJECT_WORKER_ID": c.WorkerID, "PORTAL_PROJECT_TOKEN_URL": c.TokenURL,
		"PORTAL_PROJECT_CLIENT_ID": c.ClientID, "PORTAL_PROJECT_CLIENT_SECRET": c.ClientSecret,
		"PORTAL_PROJECT_SNAPSHOTS_URL": c.SnapshotsURL,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if strings.TrimSpace(c.Scope) != requiredProjectScope {
		return fmt.Errorf("PORTAL_PROJECT_SCOPE must be %s", requiredProjectScope)
	}
	if c.PollInterval <= 0 || c.SyncInterval <= 0 || c.RetryInterval <= 0 || c.LeaseDuration < c.RequestTimeout || c.PageSize < 1 || c.PageSize > 500 || c.TokenTimeout <= 0 || c.RequestTimeout <= 0 {
		return fmt.Errorf("invalid Portal project worker scheduling or timeout configuration")
	}
	for key, raw := range map[string]string{"PORTAL_PROJECT_TOKEN_URL": c.TokenURL, "PORTAL_PROJECT_SNAPSHOTS_URL": c.SnapshotsURL} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL without credentials or fragment", key)
		}
	}
	if err := c.TLS.ValidateEndpoints(c.TokenURL, c.SnapshotsURL); err != nil {
		return fmt.Errorf("Portal project TLS: %w", err)
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
