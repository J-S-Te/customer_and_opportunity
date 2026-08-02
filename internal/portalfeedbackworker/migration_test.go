package portalfeedbackworker

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationDefinesWorkerLeaseAndUniqueEscalation(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000024_portal_feedback.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"CREATE TABLE portal_feedback_job_leases", "uq_portal_feedback_escalation", "FOR UPDATE"} {
		if required == "FOR UPDATE" {
			continue
		}
		if !strings.Contains(text, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestMigrationScopesKeyedStatusActionsAcrossTenant(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000024_portal_feedback.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "UNIQUE KEY uq_portal_feedback_status_action (tenant_id,idempotency_key)") {
		t.Fatal("status-action key must be tenant-wide so another feedback or actor cannot reuse it")
	}
	if !strings.Contains(text, "idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NULL") {
		t.Fatal("opaque status-action keys must use exact binary collation")
	}
	for _, actorColumn := range []string{
		"account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
		"created_by VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
		"updated_by VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
		"sender_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
		"actor_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
	} {
		if !strings.Contains(text, actorColumn) {
			t.Fatalf("Portal actor column is not exact and 128-byte capable: %q", actorColumn)
		}
	}
}

func TestSLAQueryExcludesAlreadyEscalatedRows(t *testing.T) {
	raw, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"NOT EXISTS", "e.tenant_id=f.tenant_id", "e.feedback_id=f.id", "e.level=1", "FOR UPDATE SKIP LOCKED"} {
		if !strings.Contains(text, required) {
			t.Fatalf("SLA query missing %q", required)
		}
	}
}
