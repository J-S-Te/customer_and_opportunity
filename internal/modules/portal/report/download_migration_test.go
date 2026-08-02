package report

import (
	"os"
	"strings"
	"testing"
)

func TestDownloadGrantMigrationEnforcesScopedActiveGrantAndImmutableAudit(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000039_portal_report_download_grants.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"CREATE TABLE portal_report_grants", "token_hash VARCHAR(64) NOT NULL",
		"UNIQUE KEY uq_portal_report_grant_active", "(tenant_id, customer_id, request_id, account_id, active_slot)",
		"FOREIGN KEY (tenant_id, customer_id, request_id)", "CREATE TABLE portal_report_download_events",
		"dedupe_key VARCHAR(64) NULL", "UNIQUE KEY uq_portal_report_download_dedupe",
		"ip_hash VARCHAR(64)", "device_hash VARCHAR(64)", "INSERT/SELECT only",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("download migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"grant_token VARCHAR", "object_ref", "plaintext"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("download migration contains forbidden %q", forbidden)
		}
	}
}

func TestDownloadGrantDownMigrationWarnsAgainstAuditLoss(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000039_portal_report_download_grants.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "Production rollback is not supported") || !strings.Contains(text, "confirming") {
		t.Fatalf("unsafe down migration: %s", text)
	}
}
