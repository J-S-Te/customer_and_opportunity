package opportunity

import (
	"os"
	"strings"
	"testing"
)

func TestOpportunityAttachmentMigrationKeepsObjectsOutOfMySQLAndFailsClosed(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000049_opportunity_attachments.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{"object_key VARCHAR(512)", "scan_status VARCHAR(32)", "uq_opportunity_attachment_create", "FOREIGN KEY (opportunity_id)", "PENDING_UPLOAD", "FINALIZING", "SCANNING", "CLEAN", "REJECTED", "SCAN_FAILED", "size_bytes <= 20971520"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"LONGBLOB", "MEDIUMBLOB", "file_content", "binary_content"} {
		if strings.Contains(strings.ToUpper(source), strings.ToUpper(forbidden)) {
			t.Fatalf("migration must not persist object bytes: %q", forbidden)
		}
	}
}
