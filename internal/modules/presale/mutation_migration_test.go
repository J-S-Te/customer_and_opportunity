package presale

import (
	"os"
	"strings"
	"testing"
)

func TestMutationIdempotencyMigrationIsLegacySafeAppendOnly(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000040_presale_mutation_idempotency.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000040_presale_mutation_idempotency.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{"ADD UNIQUE KEY uq_presale_request_tenant_id (tenant_id,id)", "CREATE TABLE crm_presale_mutation_replays", "UNIQUE KEY uq_presale_mutation_key (tenant_id,idempotency_key)", "request_hash CHAR(64) NOT NULL", "chk_presale_mutation_operation_action", "operation = 'APPROVAL_ACTION'", "action = 'REPLACE'", "fk_presale_mutation_request FOREIGN KEY (tenant_id,request_id)", "REFERENCES crm_presale_requests(tenant_id,id)", "NODE_1_PASS", "NODE_2_REJECT", "No historical rows are synthesized"} {
		if !strings.Contains(text, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE crm_presale_requests", "UPDATE crm_presale_assignments", "INSERT INTO crm_presale_mutation_replays"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("up migration must not synthesize or rewrite history: %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS crm_presale_mutation_replays") {
		t.Fatal("down migration must remove the new empty coordination table")
	}
	if !strings.Contains(string(down), "DROP INDEX uq_presale_request_tenant_id") {
		t.Fatal("down migration must remove the composite parent index after the child table")
	}
}
