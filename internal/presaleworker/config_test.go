package presaleworker

import (
	"strings"
	"testing"
)

func setValidConfig(t *testing.T) {
	t.Helper()
	t.Setenv("MYSQL_DSN", "crm:test@tcp(localhost:3306)/crm")
	t.Setenv("APPROVAL_TOKEN_URL", "https://identity.example/token")
	t.Setenv("APPROVAL_CLIENT_ID", "approval-client")
	t.Setenv("APPROVAL_CLIENT_SECRET", "secret")
	t.Setenv("APPROVAL_START_URL", "https://approval.example/start")
	t.Setenv("APPROVAL_ACTION_URL", "https://approval.example/action")
	t.Setenv("PMS_TOKEN_URL", "https://identity.example/token")
	t.Setenv("PMS_CLIENT_ID", "pms-client")
	t.Setenv("PMS_CLIENT_SECRET", "secret")
	t.Setenv("PMS_WORKLOG_URL", "https://pms.example/worklogs")
	t.Setenv("TEMPORAL_WORKER_DEPLOYMENT_NAME", "customer-opportunity-presale")
	t.Setenv("TEMPORAL_WORKER_BUILD_ID", "presale-worker-v1")
	t.Setenv("TEMPORAL_WORKER_VERSIONING_ENABLED", "true")
	t.Setenv("TEMPORAL_WORKER_VERSIONING_POLICY", "PINNED")
	t.Setenv("TEMPORAL_METRICS_ADDRESS", ":9093")
}

func TestLoadConfigHeartbeatWindowCoversPollTimeoutAndJitter(t *testing.T) {
	setValidConfig(t)
	t.Setenv("PRESALE_WORKER_POLL_INTERVAL", "10s")
	t.Setenv("PRESALE_WORKER_HEARTBEAT_MAX_AGE", "15s")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "integration timeout and jitter") {
		t.Fatalf("short freshness window error=%v", err)
	}
	t.Setenv("PRESALE_WORKER_HEARTBEAT_MAX_AGE", "20s")
	config, err := LoadConfig()
	if err != nil || config.HeartbeatMaxAge.String() != "20s" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestLoadConfigRejectsInsecureEndpointsByDefault(t *testing.T) {
	setValidConfig(t)
	t.Setenv("APPROVAL_TOKEN_URL", "http://identity.local/token")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure endpoint was accepted without the explicit dev switch: %v", err)
	}
}

func TestLoadConfigAllowsInsecureHTTPOnlyWhenExplicitlyEnabled(t *testing.T) {
	setValidConfig(t)
	for key, value := range map[string]string{
		"APPROVAL_TOKEN_URL":  "http://identity.local/token",
		"APPROVAL_START_URL":  "http://approval.local/start",
		"APPROVAL_ACTION_URL": "http://approval.local/action",
		"PMS_TOKEN_URL":       "http://identity.local/token",
		"PMS_WORKLOG_URL":     "http://pms.local/worklogs",
	} {
		t.Setenv(key, value)
	}
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PRESALE_WORKER_ALLOW_INSECURE_HTTP=true") {
		t.Fatalf("insecure endpoints were accepted without the explicit dev switch: %v", err)
	}
	t.Setenv("PRESALE_WORKER_ALLOW_INSECURE_HTTP", "true")
	config, err := LoadConfig()
	if err != nil || !config.AllowInsecureHTTP {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestLoadConfigAllowsInternalTemporalModeWithoutExternalEndpoints(t *testing.T) {
	t.Setenv("MYSQL_DSN", "crm:test@tcp(localhost:3306)/crm")
	t.Setenv("PRESALE_TEMPORAL_ENABLED", "true")
	t.Setenv("PRESALE_TEMPORAL_INTERNAL_MODE", "true")
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("internal Temporal mode rejected missing external endpoints: %v", err)
	}
	if !config.Temporal.Enabled || !config.Temporal.Internal {
		t.Fatalf("unexpected Temporal config: %+v", config.Temporal)
	}
	if !config.Temporal.Versioning || config.Temporal.DeploymentName != "customer-opportunity-presale" || config.Temporal.BuildID != "presale-worker-v1" || config.Temporal.MetricsAddress != ":9093" {
		t.Fatalf("unexpected Temporal versioning config: %+v", config.Temporal)
	}
}

func TestLoadConfigRejectsInvalidTemporalWorkerPolicy(t *testing.T) {
	setValidConfig(t)
	t.Setenv("TEMPORAL_WORKER_VERSIONING_POLICY", "LATEST")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "TEMPORAL_WORKER_VERSIONING_POLICY") {
		t.Fatalf("error=%v", err)
	}
}

func TestLoadConfigRejectsInternalModeWithoutTemporal(t *testing.T) {
	t.Setenv("MYSQL_DSN", "crm:test@tcp(localhost:3306)/crm")
	t.Setenv("PRESALE_TEMPORAL_INTERNAL_MODE", "true")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "requires PRESALE_TEMPORAL_ENABLED") {
		t.Fatalf("internal mode without Temporal was accepted: %v", err)
	}
}
