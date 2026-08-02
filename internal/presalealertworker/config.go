package presalealertworker

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	MySQLDSN      string
	WorkerID      string
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
}

func LoadConfig() (Config, error) {
	hostname, _ := os.Hostname()
	config := Config{MySQLDSN: os.Getenv("MYSQL_DSN"), WorkerID: env("PRESALE_ALERT_WORKER_ID", hostname), PollInterval: durationEnv("PRESALE_ALERT_POLL_INTERVAL", 10*time.Minute), LeaseDuration: durationEnv("PRESALE_ALERT_LEASE_DURATION", time.Minute), BatchSize: intEnv("PRESALE_ALERT_BATCH_SIZE", 200)}
	if config.MySQLDSN == "" || config.WorkerID == "" || len(config.WorkerID) > 128 || config.PollInterval <= 0 || config.LeaseDuration < 10*time.Second || config.BatchSize < 1 || config.BatchSize > 1000 {
		return Config{}, fmt.Errorf("invalid presale alert worker configuration")
	}
	return config, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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
