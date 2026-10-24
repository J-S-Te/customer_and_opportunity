package portalreportworker

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

// 报告 Worker 使用独立来源读取凭据；完整投递模式另需外部报告服务 report.request.write 凭据。
type Config struct {
	MySQLDSN                                                                          string
	WorkerID                                                                          string
	PollInterval                                                                      time.Duration
	LeaseDuration                                                                     time.Duration
	BatchSize                                                                         int
	IngestDescriptorKey                                                               []byte
	Project                                                                           ProjectServiceConfig
	IngestOnly                                                                        bool
	TenantID                                                                          string
	ReportStoreRoot                                                                   string
	ReportContentKey                                                                  []byte
	FileGatewayURL, FileGatewayTokenURL, FileGatewayClientID, FileGatewayClientSecret string
	FileGatewayTLS                                                                    integrationhttp.TLSOptions
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
	ingestDescriptorKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64"))
	if err != nil || len(ingestDescriptorKey) != 32 {
		return Config{}, fmt.Errorf("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64 must decode to 32 bytes")
	}
	ingestOnly, err := strconv.ParseBool(env("PORTAL_REPORT_WORKER_INGEST_ONLY", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid Portal report worker mode")
	}
	var requireMTLS bool
	if !ingestOnly {
		requireMTLS, err = strconv.ParseBool(env("PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS", "false"))
		if err != nil {
			return Config{}, fmt.Errorf("PORTAL_REPORT_PROJECT_TLS_REQUIRE_MTLS: %w", err)
		}
	}
	reportKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_REPORT_CONTENT_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid Portal report content key")
	}
	cfg := Config{
		MySQLDSN:            os.Getenv("PORTAL_REPORT_WORKER_MYSQL_DSN"),
		WorkerID:            env("PORTAL_REPORT_WORKER_ID", hostname()),
		PollInterval:        durationEnv("PORTAL_REPORT_WORKER_POLL_INTERVAL", time.Second),
		LeaseDuration:       durationEnv("PORTAL_REPORT_WORKER_LEASE_DURATION", 30*time.Second),
		BatchSize:           intEnv("PORTAL_REPORT_WORKER_BATCH_SIZE", 20),
		IngestDescriptorKey: ingestDescriptorKey,
		IngestOnly:          ingestOnly,
		TenantID:            os.Getenv("PORTAL_OIDC_TENANT_ID"), ReportStoreRoot: os.Getenv("PORTAL_REPORT_STORE_ROOT"), ReportContentKey: reportKey,
		FileGatewayURL: os.Getenv("PORTAL_REPORT_SOURCE_BASE_URL"), FileGatewayTokenURL: os.Getenv("PORTAL_REPORT_SOURCE_TOKEN_URL"), FileGatewayClientID: os.Getenv("PORTAL_REPORT_SOURCE_CLIENT_ID"), FileGatewayClientSecret: os.Getenv("PORTAL_REPORT_SOURCE_CLIENT_SECRET"),
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
	if cfg.TenantID == "" || !filepath.IsAbs(cfg.ReportStoreRoot) || len(cfg.ReportContentKey) != 32 || string(cfg.ReportContentKey) == string(cfg.IngestDescriptorKey) || cfg.FileGatewayURL == "" || cfg.FileGatewayTokenURL == "" || cfg.FileGatewayClientID == "" || cfg.FileGatewayClientSecret == "" {
		return Config{}, fmt.Errorf("Portal report file ingestion dependencies are incomplete")
	}
	for _, endpoint := range []string{cfg.FileGatewayURL, cfg.FileGatewayTokenURL} {
		parsed, err := url.ParseRequestURI(endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return Config{}, fmt.Errorf("Portal report source endpoints must use HTTP(S)")
		}
	}
	if err := cfg.FileGatewayTLS.ValidateEndpoints(cfg.FileGatewayURL, cfg.FileGatewayTokenURL); err != nil {
		return Config{}, fmt.Errorf("Portal report file gateway TLS: %w", err)
	}
	if cfg.IngestOnly {
		return cfg, nil
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
