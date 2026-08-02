package filing

import (
	"os"
	"strings"
	"testing"
)

func TestSubmissionReceiptMigrationKeepsLocalSnapshotsUnconfirmed(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000066_portal_filing_submission_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE TABLE portal_filing_submission_receipts",
		"uq_portal_filing_receipt_submission",
		"uq_portal_filing_receipt_external",
		"provider_evidence_sha256",
		"provider_evidence_cipher MEDIUMBLOB NOT NULL",
		"(provider_authority,provider_receipt_id)",
		"fk_portal_filing_receipt_submission",
		"'SUBMITTING','SUBMISSION_FAILED'",
		"'DEAD_LETTER','CANCELED'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "UPDATE PORTAL_FILINGS") || strings.Contains(strings.ToUpper(sql), "INSERT INTO PORTAL_FILING_SUBMISSION_RECEIPTS") {
		t.Fatal("migration must not infer a provider receipt for existing filings")
	}
}
