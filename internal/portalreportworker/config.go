package portalreportworker

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

// Config is intentionally independent from portal-server. The worker gets a
// dedicated OAuth client with only report.request.write.
type Config struct {
	MySQLDSN            string
	WorkerID            string
	PollInterval        time.Duration
	LeaseDuration       time.Duration
	BatchSize           int
	IngestDescriptorKey []byte
	Project             ProjectServiceConfig
}

type ProjectServiceConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	RequestURL   string
	TLS          integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	requireMTLS, err := strconv.ParseBool(env("PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS: %w", err)
	}
	ingestDescriptorKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64"))
	if err != nil || len(ingestDescriptorKey) != 32 {
		return Config{}, fmt.Errorf("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64 must decode to 32 bytes")
	}
	cfg := Config{
		MySQLDSN:            os.Getenv("PORTAL_REPORT_WORKER_MYSQL_DSN"),
		WorkerID:            env("PORTAL_REPORT_WORKER_ID", hostname()),
		PollInterval:        durationEnv("PORTAL_REPORT_WORKER_POLL_INTERVAL", time.Second),
		LeaseDuration:       durationEnv("PORTAL_REPORT_WORKER_LEASE_DURATION", 30*time.Second),
		BatchSize:           intEnv("PORTAL_REPORT_WORKER_BATCH_SIZE", 20),
		IngestDescriptorKey: ingestDescriptorKey,
		Project: ProjectServiceConfig{
			TokenURL: os.Getenv("PORTAL_REPORT_PROJECT_TOKEN_URL"), ClientID: os.Getenv("PORTAL_REPORT_PROJECT_CLIENT_ID"),
			ClientSecret: os.Getenv("PORTAL_REPORT_PROJECT_CLIENT_SECRET"), Scope: env("PORTAL_REPORT_PROJECT_SCOPE", "report.request.write"),
			RequestURL: os.Getenv("PORTAL_REPORT_PROJECT_REQUEST_URL"),
			TLS: integrationhttp.TLSOptions{
				RootCAFile: os.Getenv("PORTAL_REPORT_PROJECT_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_REPORT_PROJECT_TLS_CLIENT_CERT_FILE"),
				ClientKeyFile: os.Getenv("PORTAL_REPORT_PROJECT_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_REPORT_PROJECT_TLS_SERVER_NAME"), RequireMTLS: requireMTLS,
			},
		},
	}
	if strings.TrimSpace(cfg.MySQLDSN) == "" {
		return Config{}, fmt.Errorf("PORTAL_REPORT_WORKER_MYSQL_DSN is required")
	}
	if cfg.WorkerID == "" || len(cfg.WorkerID) > 128 || cfg.PollInterval <= 0 || cfg.LeaseDuration < 10*time.Second || cfg.BatchSize < 1 || cfg.BatchSize > 100 {
		return Config{}, fmt.Errorf("invalid Portal report worker scheduling configuration")
	}
	if strings.TrimSpace(cfg.Project.ClientID) == "" || strings.TrimSpace(cfg.Project.ClientSecret) == "" {
		return Config{}, fmt.Errorf("Portal report project-service OAuth credentials are required")
	}
	if strings.Join(strings.Fields(cfg.Project.Scope), " ") != "report.request.write" {
		return Config{}, fmt.Errorf("PORTAL_REPORT_PROJECT_SCOPE must be report.request.write")
	}
	for name, value := range map[string]string{"PORTAL_REPORT_PROJECT_TOKEN_URL": cfg.Project.TokenURL, "PORTAL_REPORT_PROJECT_REQUEST_URL": cfg.Project.RequestURL} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL", name)
		}
	}
	if err := cfg.Project.TLS.ValidateEndpoints(cfg.Project.TokenURL, cfg.Project.RequestURL); err != nil {
		return Config{}, fmt.Errorf("Portal report project-service TLS: %w", err)
	}
	return cfg, nil
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
