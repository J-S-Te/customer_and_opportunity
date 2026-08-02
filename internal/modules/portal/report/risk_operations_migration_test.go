package report

import (
	"os"
	"strings"
	"testing"
)

func TestRiskOperationsMigrationRequiresScopedEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000068_portal_report_risk_operations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, fragment := range []string{
		"CREATE TABLE portal_report_risk_alerts", "uq_portal_report_risk_alert_open",
		"REFERENCES portal_report_grants (tenant_id,customer_id,request_id,id)",
		"CREATE TABLE portal_report_risk_review_events", "uq_portal_report_risk_review_key",
		"CHECK (status IN ('OPEN','RESOLVED'))", "'REVOKE_AND_REISSUE'",
		"does not synthesize historical alerts",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("risk operations migration missing %q", fragment)
		}
	}
	if strings.Contains(text, "INSERT INTO portal_report_risk_alerts") {
		t.Fatal("migration invented historical risk evidence")
	}
}
