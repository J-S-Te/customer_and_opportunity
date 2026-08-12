package presaleworker

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

// 审批引擎与 PMS 使用相互独立的 OAuth 客户端，单个机器凭据不能同时获得两类集成权限。
type Config struct {
	MySQLDSN          string
	WorkerID          string
	PollInterval      time.Duration
	LeaseDuration     time.Duration
	HeartbeatMaxAge   time.Duration
	BatchSize         int
	AllowInsecureHTTP bool
	Approval          HTTPPortConfig
	PMS               HTTPPortConfig
	Temporal          TemporalConfig
}

type TemporalConfig struct {
	Enabled   bool
	Internal  bool
	Address   string
	Namespace string
	TaskQueue string
	TLS       bool
}

type HTTPPortConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	StartURL     string
	ActionURL    string
	PublishURL   string
	TLS          integrationhttp.TLSOptions
}

func LoadConfig() (Config, error) {
	approvalRequireMTLS, err := boolEnv("APPROVAL_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, fmt.Errorf("APPROVAL_TLS_REQUIRE_MTLS: %w", err)
	}
	pmsRequireMTLS, err := boolEnv("PMS_TLS_REQUIRE_MTLS", false)
	if err != nil {
		return Config{}, fmt.Errorf("PMS_TLS_REQUIRE_MTLS: %w", err)
	}
	allowInsecureHTTP, err := boolEnv("PRESALE_WORKER_ALLOW_INSECURE_HTTP", false)
	if err != nil {
		return Config{}, fmt.Errorf("PRESALE_WORKER_ALLOW_INSECURE_HTTP: %w", err)
	}
	temporalEnabled, err := boolEnv("PRESALE_TEMPORAL_ENABLED", false)
	if err != nil {
		return Config{}, fmt.Errorf("PRESALE_TEMPORAL_ENABLED: %w", err)
	}
	temporalTLS, err := boolEnv("TEMPORAL_TLS", false)
	if err != nil {
		return Config{}, fmt.Errorf("TEMPORAL_TLS: %w", err)
	}
	temporalInternal, err := boolEnv("PRESALE_TEMPORAL_INTERNAL_MODE", false)
	if err != nil {
		return Config{}, fmt.Errorf("PRESALE_TEMPORAL_INTERNAL_MODE: %w", err)
	}
	cfg := Config{
		MySQLDSN:          os.Getenv("MYSQL_DSN"),
		WorkerID:          env("PRESALE_WORKER_ID", hostname()),
		PollInterval:      durationEnv("PRESALE_WORKER_POLL_INTERVAL", time.Second),
		LeaseDuration:     durationEnv("PRESALE_WORKER_LEASE_DURATION", 30*time.Second),
		HeartbeatMaxAge:   durationEnv("PRESALE_WORKER_HEARTBEAT_MAX_AGE", 15*time.Second),
		BatchSize:         intEnv("PRESALE_WORKER_BATCH_SIZE", 20),
		AllowInsecureHTTP: allowInsecureHTTP,
		Approval: HTTPPortConfig{TokenURL: os.Getenv("APPROVAL_TOKEN_URL"), ClientID: os.Getenv("APPROVAL_CLIENT_ID"), ClientSecret: os.Getenv("APPROVAL_CLIENT_SECRET"), Scope: env("APPROVAL_SCOPE", "presale.approval.write"), StartURL: os.Getenv("APPROVAL_START_URL"), ActionURL: os.Getenv("APPROVAL_ACTION_URL"), TLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("APPROVAL_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("APPROVAL_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("APPROVAL_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("APPROVAL_TLS_SERVER_NAME"), RequireMTLS: approvalRequireMTLS,
		}},
		PMS: HTTPPortConfig{TokenURL: os.Getenv("PMS_TOKEN_URL"), ClientID: os.Getenv("PMS_CLIENT_ID"), ClientSecret: os.Getenv("PMS_CLIENT_SECRET"), Scope: env("PMS_SCOPE", "presale.worklog.write"), PublishURL: os.Getenv("PMS_WORKLOG_URL"), TLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("PMS_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PMS_TLS_CLIENT_CERT_FILE"), ClientKeyFile: os.Getenv("PMS_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PMS_TLS_SERVER_NAME"), RequireMTLS: pmsRequireMTLS,
		}},
		Temporal: TemporalConfig{Enabled: temporalEnabled, Internal: temporalInternal, Address: env("TEMPORAL_ADDRESS", "temporal:7233"), Namespace: env("TEMPORAL_NAMESPACE", "default"), TaskQueue: env("TEMPORAL_TASK_QUEUE", "customer-opportunity-presale"), TLS: temporalTLS},
	}
	if cfg.MySQLDSN == "" {
		return Config{}, fmt.Errorf("MYSQL_DSN is required")
	}
	if cfg.WorkerID == "" || cfg.PollInterval <= 0 || cfg.LeaseDuration < 10*time.Second || cfg.BatchSize < 1 || cfg.BatchSize > 100 {
		return Config{}, fmt.Errorf("invalid worker scheduling configuration")
	}
	// HTTP 端口固定五秒超时；逐事件刷新心跳并预留调度/数据库抖动，降低活跃 Worker 被误判失联的概率。
	if cfg.HeartbeatMaxAge < cfg.PollInterval+10*time.Second || cfg.HeartbeatMaxAge > 5*time.Minute {
		return Config{}, fmt.Errorf("PRESALE_WORKER_HEARTBEAT_MAX_AGE must cover poll interval, integration timeout and jitter")
	}
	if !cfg.Temporal.Internal {
		if err := validatePort("approval", cfg.Approval, true, allowInsecureHTTP); err != nil {
			return Config{}, err
		}
		if err := validatePort("PMS", cfg.PMS, false, allowInsecureHTTP); err != nil {
			return Config{}, err
		}
	} else if !cfg.Temporal.Enabled {
		return Config{}, fmt.Errorf("PRESALE_TEMPORAL_INTERNAL_MODE requires PRESALE_TEMPORAL_ENABLED=true")
	}
	if cfg.Temporal.Enabled && (strings.TrimSpace(cfg.Temporal.Address) == "" || strings.TrimSpace(cfg.Temporal.Namespace) == "" || strings.TrimSpace(cfg.Temporal.TaskQueue) == "") {
		return Config{}, fmt.Errorf("Temporal address, namespace and task queue are required when PRESALE_TEMPORAL_ENABLED=true")
	}
	return cfg, nil
}

func validatePort(name string, cfg HTTPPortConfig, approval bool, allowInsecureHTTP bool) error {
	required := map[string]string{"token URL": cfg.TokenURL, "client ID": cfg.ClientID, "client secret": cfg.ClientSecret, "scope": cfg.Scope}
	if approval {
		required["start URL"], required["action URL"] = cfg.StartURL, cfg.ActionURL
	} else {
		required["publish URL"] = cfg.PublishURL
	}
	for label, value := range required {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s %s is required", name, label)
		}
	}
	expectedScope := "presale.worklog.write"
	endpoints := []string{cfg.TokenURL, cfg.PublishURL}
	if approval {
		expectedScope = "presale.approval.write"
		endpoints = []string{cfg.TokenURL, cfg.StartURL, cfg.ActionURL}
	}
	if cfg.Scope != expectedScope {
		return fmt.Errorf("%s scope must be %s", name, expectedScope)
	}
	for _, endpoint := range endpoints {
		parsed, err := url.ParseRequestURI(endpoint)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s endpoint must be a valid URL without credentials, query or fragment", name)
		}
		if parsed.Scheme != "https" && !allowInsecureHTTP {
			return fmt.Errorf("%s endpoint must use HTTPS unless PRESALE_WORKER_ALLOW_INSECURE_HTTP=true", name)
		}
	}
	if err := cfg.TLS.ValidateEndpoints(endpoints...); err != nil {
		return fmt.Errorf("%s TLS: %w", name, err)
	}
	return nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseBool(value)
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
