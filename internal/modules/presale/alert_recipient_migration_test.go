package presale

import (
	"os"
	"strings"
	"testing"
)

func TestAlertRecipientMigrationFailsLegacyNamespaceClosed(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000071_presale_alert_recipient_namespace.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"recipient_kind", "'USER','PERSON','LEGACY_UNKNOWN'", "migration-000071", "status='PENDING'", "e.status='CANCELLED'", "created_by VARCHAR(128)", "updated_by VARCHAR(128)", "recipient_id VARCHAR(128)", "utf8mb4_bin", "DROP DEFAULT"} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"SET recipient_kind='USER'", "SET recipient_kind='PERSON'"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("migration guessed legacy recipient namespace: %q", forbidden)
		}
	}
}
