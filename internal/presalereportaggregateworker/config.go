package presalereportaggregateworker

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
	PollInterval  time.Duration
	LeaseDuration time.Duration
	LookbackDays  int
	TenantIDs     []string
}

func LoadConfig() (Config, error) {
	hostname, _ := os.Hostname()
	tenants, tenantErr := tenantIDs(os.Getenv("PRESALE_REPORT_AGGREGATE_TENANTS"))
	if tenantErr != nil {
		return Config{}, tenantErr
	}
	config := Config{
		MySQLDSN:      strings.TrimSpace(os.Getenv("MYSQL_DSN")),
		WorkerID:      env("PRESALE_REPORT_AGGREGATE_WORKER_ID", hostname),
		PollInterval:  durationEnv("PRESALE_REPORT_AGGREGATE_POLL_INTERVAL", time.Hour),
		LeaseDuration: durationEnv("PRESALE_REPORT_AGGREGATE_LEASE_DURATION", 5*time.Minute),
		LookbackDays:  intEnv("PRESALE_REPORT_AGGREGATE_LOOKBACK_DAYS", 32),
		TenantIDs:     tenants,
	}
	if config.MySQLDSN == "" || config.WorkerID == "" || len(config.WorkerID) > 128 ||
		config.PollInterval < time.Minute || config.LeaseDuration < 30*time.Second ||
		config.LookbackDays < 1 || config.LookbackDays > 366 {
		return Config{}, fmt.Errorf("invalid presale report aggregate worker configuration")
	}
	return config, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
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
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func tenantIDs(raw string) ([]string, error) {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) > 64 {
			return nil, fmt.Errorf("invalid presale report aggregate tenant identifier")
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		values = append(values, item)
	}
	return values, nil
}
