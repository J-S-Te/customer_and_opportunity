package contracttransferworker

import (
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
	TokenURL      string
	ClientID      string
	ClientSecret  string
	Scope         string
	IntakeURL     string
	TLS           integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	hostname, _ := os.Hostname()
	requireMTLS, err := boolEnv("CONTRACT_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		MySQLDSN: os.Getenv("MYSQL_DSN"), WorkerID: env("CONTRACT_TRANSFER_WORKER_ID", hostname),
		PollInterval:  durationEnv("CONTRACT_TRANSFER_POLL_INTERVAL", 5*time.Second),
		LeaseDuration: durationEnv("CONTRACT_TRANSFER_LEASE_DURATION", time.Minute),
		BatchSize:     intEnv("CONTRACT_TRANSFER_BATCH_SIZE", 20), TokenURL: os.Getenv("CONTRACT_TOKEN_URL"),
		ClientID: os.Getenv("CONTRACT_CLIENT_ID"), ClientSecret: os.Getenv("CONTRACT_CLIENT_SECRET"),
		Scope: env("CONTRACT_SCOPE", "opportunity.signed.write"), IntakeURL: os.Getenv("CONTRACT_OPPORTUNITY_INTAKE_URL"),
		TLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("CONTRACT_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("CONTRACT_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("CONTRACT_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("CONTRACT_TLS_SERVER_NAME"),
			RequireMTLS: requireMTLS,
		},
	}
	if cfg.MySQLDSN == "" || cfg.WorkerID == "" || len(cfg.WorkerID) > 128 || cfg.PollInterval <= 0 ||
		cfg.LeaseDuration < 15*time.Second || cfg.BatchSize < 1 || cfg.BatchSize > 100 ||
		cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Scope != "opportunity.signed.write" {
		return Config{}, fmt.Errorf("invalid contract transfer worker configuration")
	}
	// 两个端点都属于机器间协议；配置阶段固定 scheme、禁止 URL 用户信息，并把是否允许明文 HTTP 交给显式安全开关。
	for name, raw := range map[string]string{"CONTRACT_TOKEN_URL": cfg.TokenURL, "CONTRACT_OPPORTUNITY_INTAKE_URL": cfg.IntakeURL} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("%s must be an HTTP(S) URL without credentials, query or fragment", name)
		}
	}
	if err := cfg.TLS.ValidateEndpoints(cfg.TokenURL, cfg.IntakeURL); err != nil {
		return Config{}, fmt.Errorf("contract transfer TLS: %w", err)
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
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
