package portalfeedbackworker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	MySQLDSN      string
	WorkerID      string
	TenantID      string
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
}

func LoadConfig() (Config, error) {
	config := Config{
		MySQLDSN:      os.Getenv("PORTAL_FEEDBACK_WORKER_MYSQL_DSN"),
		WorkerID:      valueOrDefault("PORTAL_FEEDBACK_WORKER_ID", hostname()),
		TenantID:      os.Getenv("PORTAL_FEEDBACK_WORKER_TENANT_ID"),
		PollInterval:  durationValue("PORTAL_FEEDBACK_WORKER_POLL_INTERVAL", time.Minute),
		LeaseDuration: durationValue("PORTAL_FEEDBACK_WORKER_LEASE_DURATION", 30*time.Second),
		BatchSize:     integerValue("PORTAL_FEEDBACK_WORKER_BATCH_SIZE", 100),
	}
	if strings.TrimSpace(config.MySQLDSN) == "" || strings.TrimSpace(config.WorkerID) == "" || len(config.WorkerID) > 128 || strings.TrimSpace(config.TenantID) == "" {
		return Config{}, fmt.Errorf("Portal feedback worker database, worker and tenant configuration are required")
	}
	if config.PollInterval <= 0 || config.LeaseDuration < 10*time.Second || config.BatchSize < 1 || config.BatchSize > 500 {
		return Config{}, fmt.Errorf("invalid Portal feedback worker scheduling configuration")
	}
	return config, nil
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func hostname() string { value, _ := os.Hostname(); return value }

func durationValue(key string, fallback time.Duration) time.Duration {
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

func integerValue(key string, fallback int) int {
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
