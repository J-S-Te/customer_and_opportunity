package projectexportworker

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	MySQLDSN, WorkerID, PDFConfigRoot, CJKTFontPath string
	PollInterval, LeaseDuration                     time.Duration
}

func LoadConfig() (Config, error) {
	cfg := Config{MySQLDSN: os.Getenv("PORTAL_PROJECT_EXPORT_MYSQL_DSN"), WorkerID: env("PORTAL_PROJECT_EXPORT_WORKER_ID", hostname()), PDFConfigRoot: os.Getenv("PORTAL_PROJECT_EXPORT_PDF_CONFIG_ROOT"), CJKTFontPath: os.Getenv("PORTAL_PROJECT_EXPORT_CJK_FONT_PATH"), PollInterval: durationEnv("PORTAL_PROJECT_EXPORT_POLL_INTERVAL", time.Second), LeaseDuration: durationEnv("PORTAL_PROJECT_EXPORT_LEASE_DURATION", 30*time.Second)}
	if strings.TrimSpace(cfg.MySQLDSN) == "" || strings.TrimSpace(cfg.WorkerID) == "" || len(cfg.WorkerID) > 128 || strings.TrimSpace(cfg.PDFConfigRoot) == "" || strings.TrimSpace(cfg.CJKTFontPath) == "" || cfg.PollInterval <= 0 || cfg.LeaseDuration < 10*time.Second {
		return Config{}, fmt.Errorf("invalid Portal project export worker configuration")
	}
	return cfg, nil
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
