package notificationdeliveryworker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 通知双写 worker 配置。未配置平台机器凭据时 worker 直接退出，不投递。
type Config struct {
	MySQLDSN         string
	WorkerID         string
	PollInterval     time.Duration
	BatchSize        int
	PlatformBaseURL  string
	PlatformTokenURL string
	ClientID         string
	ClientSecret     string
	ApplicationCode  string
	EnvironmentCode  string
}

func LoadConfig() (Config, error) {
	hostname, _ := os.Hostname()
	config := Config{
		MySQLDSN:         os.Getenv("MYSQL_DSN"),
		WorkerID:         env("NOTIFICATION_DELIVERY_WORKER_ID", hostname),
		PollInterval:     durationEnv("NOTIFICATION_DELIVERY_POLL_INTERVAL", 5*time.Second),
		BatchSize:        intEnv("NOTIFICATION_DELIVERY_BATCH_SIZE", 100),
		PlatformBaseURL:  strings.TrimRight(os.Getenv("PLATFORM_BASE_URL"), "/"),
		PlatformTokenURL: strings.TrimRight(os.Getenv("PLATFORM_BASE_URL"), "/") + "/oauth2/token",
		ClientID:         os.Getenv("PLATFORM_NOTIFICATION_CLIENT_ID"),
		ClientSecret:     os.Getenv("PLATFORM_NOTIFICATION_CLIENT_SECRET"),
		ApplicationCode:  env("PLATFORM_APPLICATION_CODE", "customer_and_opportunity"),
		EnvironmentCode:  env("PLATFORM_ENVIRONMENT_CODE", "dev"),
	}
	if config.MySQLDSN == "" || config.WorkerID == "" || len(config.WorkerID) > 128 || config.PollInterval <= 0 ||
		config.BatchSize < 1 || config.BatchSize > 1000 {
		return Config{}, fmt.Errorf("invalid notification delivery worker configuration")
	}
	return config, nil
}

// Enabled 表示是否配置了平台机器凭据，未配置时不应启动投递。
func (c Config) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.PlatformBaseURL != ""
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
