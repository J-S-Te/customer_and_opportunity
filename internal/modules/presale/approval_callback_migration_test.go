package presale

import (
	"os"
	"strings"
	"testing"
)

func TestApprovalCallbackReplayMigrationPersistsAuthoritativeIdentity(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000065_presale_approval_callback_replay_binding.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{"engine_instance_id VARCHAR(128)", "event_sequence BIGINT UNSIGNED", "idx_presale_approval_instance_sequence"} {
		if !strings.Contains(text, required) {
			t.Fatalf("approval callback migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE crm_presale_approval_logs", "INSERT INTO crm_presale_approval_logs"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("migration must not synthesize historical callback evidence: %q", forbidden)
		}
	}
}
