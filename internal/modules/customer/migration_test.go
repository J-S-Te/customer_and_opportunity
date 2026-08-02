package customer

import (
	"os"
	"strings"
	"testing"
)

func TestCustomerQueryMigrationAddsAndDropsNamedIndexes(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000032_customer_query_indexes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000032_customer_query_indexes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"ALTER TABLE crm_customers",
		"ADD KEY idx_customer_created (tenant_id, created_at, id)",
		"ALTER TABLE crm_customer_followups",
		"ADD KEY idx_customer_followup_due (tenant_id, next_follow_at, customer_id, followed_at, id)",
	} {
		if !strings.Contains(string(up), fragment) {
			t.Fatalf("up migration missing %q", fragment)
		}
	}
	for _, fragment := range []string{"DROP KEY idx_customer_followup_due", "DROP KEY idx_customer_created"} {
		if !strings.Contains(string(down), fragment) {
			t.Fatalf("down migration missing %q", fragment)
		}
	}
}

func TestCustomerProfileMigrationCreatesScopedProtectedChildrenAndDown(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000033_customer_profile_children.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000033_customer_profile_children.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"ADD UNIQUE KEY uq_customer_tenant_id (tenant_id,id)",
		"CREATE TABLE crm_customer_stakeholders", "tenant_id VARCHAR(64) NOT NULL", "customer_id BIGINT UNSIGNED NOT NULL",
		"phone_cipher VARBINARY(512) NULL", "email_cipher VARBINARY(512) NULL", "CHECK (influence IN ('LOW','MEDIUM','HIGH'))",
		"CREATE TABLE crm_customer_systems", "CHECK (protection_level IN ('LEVEL_1','LEVEL_2','LEVEL_3','LEVEL_4','LEVEL_5'))",
		"CHECK (filing_status IN ('NOT_FILED','FILING','FILED'))", "FOREIGN KEY (tenant_id,customer_id) REFERENCES crm_customers(tenant_id,id)",
	} {
		if !strings.Contains(string(up), fragment) {
			t.Fatalf("up migration missing %q", fragment)
		}
	}
	for _, fragment := range []string{"DROP TABLE IF EXISTS crm_customer_systems", "DROP TABLE IF EXISTS crm_customer_stakeholders", "DROP KEY uq_customer_tenant_id"} {
		if !strings.Contains(string(down), fragment) {
			t.Fatalf("down migration missing %q", fragment)
		}
	}
	if strings.Contains(strings.ToLower(string(up)), "credit_rating") || strings.Contains(string(up), "信用评级") {
		t.Fatal("removed customer credit-rating concept leaked into profile migration")
	}
}

func TestCustomerImportMigrationKeepsOnlyEncryptedCommandsAndScopedCoordination(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000034_customer_imports.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000034_customer_imports.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"CREATE TABLE crm_customer_import_jobs", "tenant_id VARCHAR(64) NOT NULL", "actor_id VARCHAR(64) NOT NULL",
		"expires_at DATETIME(3) NOT NULL", "locked_by VARCHAR(128)", "locked_until DATETIME(3)",
		"CREATE TABLE crm_customer_import_rows", "command_cipher VARBINARY(4096) NULL",
		"FOREIGN KEY (tenant_id,job_id) REFERENCES crm_customer_import_jobs(tenant_id,id)",
		"CREATE TABLE crm_customer_import_idempotency", "UNIQUE KEY uq_customer_import_idempotency (tenant_id,actor_id,idempotency_key)",
	} {
		if !strings.Contains(string(up), fragment) {
			t.Fatalf("up migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"file_bytes", "phone_plain", "email_plain", "credit_code_plain"} {
		if strings.Contains(strings.ToLower(string(up)), forbidden) {
			t.Fatalf("migration stores forbidden import data %q", forbidden)
		}
	}
	for _, fragment := range []string{"DROP TABLE IF EXISTS crm_customer_import_idempotency", "DROP TABLE IF EXISTS crm_customer_import_rows", "DROP TABLE IF EXISTS crm_customer_import_jobs"} {
		if !strings.Contains(string(down), fragment) {
			t.Fatalf("down migration missing %q", fragment)
		}
	}
}

func TestCustomerCreateIdempotencyMigrationIsScopedProtectedAndReversible(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000044_customer_create_idempotency.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000044_customer_create_idempotency.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"CREATE TABLE crm_customer_create_idempotency", "request_hash CHAR(64) NOT NULL", "response_json JSON NOT NULL",
		"tenant_id VARCHAR(64) NOT NULL", "actor_id VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL",
		"idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL",
		"status VARCHAR(16) COLLATE utf8mb4_0900_bin NOT NULL", "response_hash CHAR(64) NOT NULL",
		"UNIQUE KEY uq_customer_create_idempotency (tenant_id,actor_id,idempotency_key)",
		"UNIQUE KEY uq_customer_create_resource (tenant_id,customer_id)",
		"CHECK (CHAR_LENGTH(TRIM(idempotency_key)) BETWEEN 1 AND 128)", "CHECK (request_hash REGEXP '^[0-9a-f]{64}$')",
		"CHECK (status = 'COMPLETED')", "CHECK (response_hash REGEXP '^[0-9a-f]{64}$')",
		"FOREIGN KEY (tenant_id,customer_id)", "REFERENCES crm_customers(tenant_id,id)", "No historical rows are synthesized",
	} {
		if !strings.Contains(string(up), fragment) {
			t.Fatalf("up migration missing %q", fragment)
		}
	}
	lower := strings.ToLower(string(up))
	for _, forbidden := range []string{"credit_code_plain", "phone_plain", "email_plain", "request_json"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("migration stores forbidden request material %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS crm_customer_create_idempotency") {
		t.Fatal("down migration does not drop the replay table")
	}
}
