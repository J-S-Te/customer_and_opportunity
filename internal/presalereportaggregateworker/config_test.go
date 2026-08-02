package presalereportaggregateworker

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigUsesBoundedDefaultsAndExplicitTenantScope(t *testing.T) {
	t.Setenv("MYSQL_DSN", "crm:secret@tcp(mysql:3306)/crm?parseTime=true")
	t.Setenv("PRESALE_REPORT_AGGREGATE_WORKER_ID", "aggregate-1")
	t.Setenv("PRESALE_REPORT_AGGREGATE_TENANTS", "tenant-b, tenant-a,tenant-b")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LookbackDays != 32 || config.PollInterval != time.Hour || config.LeaseDuration != 5*time.Minute {
		t.Fatalf("unexpected defaults: %#v", config)
	}
	if len(config.TenantIDs) != 2 || config.TenantIDs[0] != "tenant-b" || config.TenantIDs[1] != "tenant-a" {
		t.Fatalf("tenant scope=%v", config.TenantIDs)
	}
}

func TestLoadConfigRejectsUnsafeBoundsAndTenantIdentifiers(t *testing.T) {
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("PRESALE_REPORT_AGGREGATE_WORKER_ID", "worker")
	for key, value := range map[string]string{
		"PRESALE_REPORT_AGGREGATE_LOOKBACK_DAYS": "367",
		"PRESALE_REPORT_AGGREGATE_POLL_INTERVAL": "30s",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
	t.Setenv("PRESALE_REPORT_AGGREGATE_LOOKBACK_DAYS", "32")
	t.Setenv("PRESALE_REPORT_AGGREGATE_POLL_INTERVAL", "1h")
	t.Setenv("PRESALE_REPORT_AGGREGATE_TENANTS", strings.Repeat("t", 65))
	if _, err := LoadConfig(); err == nil {
		t.Fatal("oversized tenant identifier was accepted")
	}
}
