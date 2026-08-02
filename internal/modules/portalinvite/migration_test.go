package portalinvite

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProvisionOperationMigrationIsScopedEncryptedAndReversible(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	up, err := os.ReadFile(filepath.Join(root, "migrations", "000064_portal_invite_operation_idempotency.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join(root, "migrations", "000064_portal_invite_operation_idempotency.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE TABLE crm_portal_provision_operations",
		"UNIQUE KEY uq_portal_provision_idempotency (tenant_id, actor_id, idempotency_key)",
		"contact_snapshot_cipher VARBINARY(4096) NOT NULL",
		"token_cipher VARBINARY(1024) NULL",
		"CHECK (stage IN ('PREPARED','USER_PROVISIONED','ROLE_ASSIGNED','MAPPING_READY','COMPLETED'))",
		"CHECK (status IN ('PROCESSING','RETRY_WAIT','COMPLETED'))",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{" phone ", " email ", " raw_token "} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("migration persists forbidden plaintext field %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS crm_portal_provision_operations") {
		t.Fatal("down migration does not remove provision operation table")
	}
}
