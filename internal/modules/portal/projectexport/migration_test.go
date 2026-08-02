package projectexport

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationBindsExportAndGrantToPortalAccount(t *testing.T) {
	content, err := os.ReadFile("../../../../migrations/000046_portal_project_exports.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{"tenant_id VARCHAR(64)", "customer_id BIGINT UNSIGNED", "account_id VARCHAR(128)", "snapshot_json JSON", "file_bytes MEDIUMBLOB", "token_hash CHAR(64)", "uq_portal_project_export_key", "portal_project_export_events"} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(text), "token varchar") {
		t.Fatal("plaintext token column must not exist")
	}
}
