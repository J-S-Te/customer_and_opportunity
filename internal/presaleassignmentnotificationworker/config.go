package presaleassignmentnotificationworker

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
	config := Config{
		MySQLDSN:      os.Getenv("MYSQL_DSN"),
		WorkerID:      env("PRESALE_ASSIGNMENT_NOTIFICATION_WORKER_ID", hostname),
		PollInterval:  durationEnv("PRESALE_ASSIGNMENT_NOTIFICATION_POLL_INTERVAL", 5*time.Second),
		LeaseDuration: durationEnv("PRESALE_ASSIGNMENT_NOTIFICATION_LEASE_DURATION", time.Minute),
		BatchSize:     intEnv("PRESALE_ASSIGNMENT_NOTIFICATION_BATCH_SIZE", 100),
	}
	if config.MySQLDSN == "" || config.WorkerID == "" || len(config.WorkerID) > 128 || config.PollInterval <= 0 ||
		config.LeaseDuration < 10*time.Second || config.BatchSize < 1 || config.BatchSize > 1000 {
		return Config{}, fmt.Errorf("invalid presale assignment notification worker configuration")
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
