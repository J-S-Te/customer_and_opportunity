package report

import (
	"os"
	"strings"
	"testing"
)

func TestStatusEventMigrationDefinesScopedImmutableTimeline(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000038_portal_report_status_events.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"CREATE TABLE portal_report_status_events",
		"(tenant_id, customer_id, request_id, sequence, id)",
		"FOREIGN KEY (tenant_id, customer_id, request_id)",
		"REFERENCES portal_report_requests (tenant_id, customer_id, id)",
		"UNIQUE KEY uq_portal_report_status_source",
		"(tenant_id, request_id, source_key_hash)",
		"payload_hash VARCHAR(64) NOT NULL DEFAULT ''",
		"UNIQUE KEY uq_portal_report_status_sequence",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("status event migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE portal_report_requests", "INSERT INTO portal_report_status_events", "DELETE FROM portal_report"} {
		if strings.Contains(strings.ToUpper(text), strings.ToUpper(forbidden)) {
			t.Fatalf("status event migration must not rewrite history: found %q", forbidden)
		}
	}
}

func TestStatusEventDownMigrationWarnsAgainstAuditLoss(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000038_portal_report_status_events.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "Production rollback is not supported") || !strings.Contains(text, "confirming the table is empty") {
		t.Fatalf("down migration lacks controlled rollback warning: %s", text)
	}
}
