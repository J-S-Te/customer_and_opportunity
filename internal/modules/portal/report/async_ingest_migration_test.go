package report

import (
	"os"
	"strings"
	"testing"
)

func TestAsyncIngestMigrationHasLeaseBindingAndNoSyntheticBackfill(t *testing.T) {
	contents, err := os.ReadFile("../../../../migrations/000063_portal_report_async_ingest.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{"portal_report_ingest_jobs", "descriptor_cipher VARBINARY(2048)", "descriptor_hash VARCHAR(64)", "uq_portal_report_ingest_request", "idx_portal_report_ingest_lease", "FOREIGN KEY (tenant_id, customer_id, request_id)", "DEAD_LETTER"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE portal_report_requests", "INSERT INTO portal_report_ingest_jobs"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration invents historical ingest state with %q", forbidden)
		}
	}
}
