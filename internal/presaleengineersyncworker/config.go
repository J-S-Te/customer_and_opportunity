package presaleengineersyncworker

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

type Config struct {
	MySQLDSN      string
	WorkerID      string
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
	SyncInterval  time.Duration
	TokenURL      string
	ClientID      string
	ClientSecret  string
	Scope         string
	Endpoint      string
	EncryptionKey []byte
	TLS           integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("PMS_ENGINEER_SYNC_ENCRYPTION_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("PMS_ENGINEER_SYNC_ENCRYPTION_KEY_BASE64: %w", err)
	}
	requireMTLS, err := strconv.ParseBool(env("PMS_ENGINEER_TLS_REQUIRE_MTLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PMS_ENGINEER_TLS_REQUIRE_MTLS: %w", err)
	}
	cfg := Config{
		MySQLDSN: os.Getenv("MYSQL_DSN"), WorkerID: env("PMS_ENGINEER_SYNC_WORKER_ID", hostname()),
		PollInterval:  durationEnv("PMS_ENGINEER_SYNC_POLL_INTERVAL", 15*time.Second),
		LeaseDuration: durationEnv("PMS_ENGINEER_SYNC_LEASE_DURATION", 2*time.Minute),
		BatchSize:     intEnv("PMS_ENGINEER_SYNC_BATCH_SIZE", 10), SyncInterval: durationEnv("PMS_ENGINEER_SYNC_INTERVAL", 6*time.Hour),
		TokenURL: os.Getenv("PMS_ENGINEER_TOKEN_URL"), ClientID: os.Getenv("PMS_ENGINEER_CLIENT_ID"),
		ClientSecret: os.Getenv("PMS_ENGINEER_CLIENT_SECRET"), Scope: env("PMS_ENGINEER_SCOPE", "technician.read"),
		Endpoint: os.Getenv("PMS_ENGINEER_URL"), EncryptionKey: key,
		TLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("PMS_ENGINEER_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PMS_ENGINEER_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("PMS_ENGINEER_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PMS_ENGINEER_TLS_SERVER_NAME"), RequireMTLS: requireMTLS,
		},
	}
	if cfg.MySQLDSN == "" || cfg.WorkerID == "" || cfg.PollInterval <= 0 || cfg.LeaseDuration < 30*time.Second || cfg.BatchSize < 1 || cfg.BatchSize > 100 || cfg.SyncInterval < time.Hour {
		return Config{}, fmt.Errorf("invalid PMS engineer sync database or scheduling configuration")
	}
	if len(cfg.EncryptionKey) != 32 {
		return Config{}, fmt.Errorf("PMS_ENGINEER_SYNC_ENCRYPTION_KEY_BASE64 must decode to 32 bytes")
	}
	for name, value := range map[string]string{"token URL": cfg.TokenURL, "client ID": cfg.ClientID, "client secret": cfg.ClientSecret, "scope": cfg.Scope, "engineer URL": cfg.Endpoint} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return Config{}, fmt.Errorf("PMS engineer %s is required", name)
		}
	}
	if cfg.Scope != "technician.read" {
		return Config{}, fmt.Errorf("PMS_ENGINEER_SCOPE must be technician.read")
	}
	for name, value := range map[string]string{"PMS_ENGINEER_TOKEN_URL": cfg.TokenURL, "PMS_ENGINEER_URL": cfg.Endpoint} {
		parsed, parseErr := url.ParseRequestURI(value)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("%s must be a valid HTTPS URL without credentials, query or fragment", name)
		}
	}
	if err := cfg.TLS.ValidateEndpoints(cfg.TokenURL, cfg.Endpoint); err != nil {
		return Config{}, fmt.Errorf("PMS engineer TLS: %w", err)
	}
	return cfg, nil
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
