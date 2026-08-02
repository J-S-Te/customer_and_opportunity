package report

import (
	"os"
	"strings"
	"testing"
)

func TestScanStatusMigrationKeepsExistingFilesUnreadable(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000059_portal_report_scan_status_evidence.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"ADD COLUMN scan_status VARCHAR(16) NOT NULL DEFAULT ''",
		"scan_status='CLEAN'", "object_version<>''", "encryption_algorithm='AES-256-GCM'",
		"scan_reference<>''", "scanned_at IS NOT NULL", "file_hash REGEXP '^[0-9a-f]{64}$'",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("scan status migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE portal_report_files", "DEFAULT 'CLEAN'"} {
		if strings.Contains(strings.ToUpper(text), strings.ToUpper(forbidden)) {
			t.Fatalf("scan status migration fabricates evidence with %q", forbidden)
		}
	}
}

func TestScanStatusDownMigrationWarnsAboutEvidenceLoss(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000059_portal_report_scan_status_evidence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "Controlled rollback only") || !strings.Contains(text, "re-ingested") {
		t.Fatalf("scan status rollback lacks evidence-loss warning: %s", text)
	}
}
