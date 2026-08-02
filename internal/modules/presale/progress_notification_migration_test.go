package presale

import (
	"os"
	"strings"
	"testing"
)

func TestProgressNotificationMigrationIsForwardOnlyAndNamespaced(t *testing.T) {
	content, err := os.ReadFile("../../../migrations/000051_presale_progress_notifications.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, required := range []string{
		"crm_presale_progress_notification_events", "recipient_namespace", "recipient_kind",
		"PRESALE_PROGRESS_APPLICANT", "PRESALE_PROGRESS_ASSIGNEE", "progress_id",
		"fk_presale_progress_notification_request", "fk_presale_progress_notification_progress",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing %q", required)
		}
	}
	if strings.Contains(sql, "INSERT INTO crm_presale_progress_notification_events") || strings.Contains(sql, "INSERT INTO crm_notifications") {
		t.Fatal("migration must not synthesize historical recipient evidence")
	}
}
