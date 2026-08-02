package presale

import (
	"os"
	"strings"
	"testing"
)

func TestWorkerHeartbeatMigrationSupportsMultiInstanceFreshnessEvidence(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000072_presale_worker_heartbeats.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000072_presale_worker_heartbeats.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	value := string(up)
	for _, required := range []string{
		"CREATE TABLE crm_worker_heartbeats", "worker_type VARCHAR(64) NOT NULL",
		"worker_id VARCHAR(128) NOT NULL", "heartbeat_at DATETIME(3) NOT NULL",
		"UNIQUE KEY uq_crm_worker_heartbeat (worker_type, worker_id)",
		"KEY idx_crm_worker_heartbeat_freshness (worker_type, heartbeat_at)",
	} {
		if !strings.Contains(value, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS crm_worker_heartbeats") {
		t.Fatal("down migration must remove worker heartbeat coordination table")
	}
}
