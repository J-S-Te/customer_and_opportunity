package presale

import (
	"os"
	"strings"
	"testing"
)

func TestProgressIdempotencyMigrationAddsNullableLegacySafeUniqueConstraint(t *testing.T) {
	t.Parallel()
	up, err := os.ReadFile("../../../migrations/000036_presale_progress_idempotency.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000036_presale_progress_idempotency.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"ADD COLUMN idempotency_key VARCHAR(128) NULL", "ADD COLUMN request_hash CHAR(64) NULL",
		"ADD UNIQUE KEY uq_presale_progress_key (tenant_id,idempotency_key)", "online-DDL", "No",
		"historical business-data backfill is required",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	if strings.Contains(text, "UPDATE crm_presale_progress_logs") || strings.Contains(text, "MODIFY COLUMN idempotency_key") {
		t.Fatal("migration must not rewrite the immutable historical progress log")
	}
	for _, required := range []string{"DROP INDEX uq_presale_progress_key", "DROP COLUMN request_hash", "DROP COLUMN idempotency_key"} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
}
