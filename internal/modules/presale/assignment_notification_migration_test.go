package presale

import (
	"os"
	"strings"
	"testing"
)

func TestAssignmentNotificationMigrationPreservesEvidenceAndPersonalIsolation(t *testing.T) {
	contents, err := os.ReadFile("../../../migrations/000042_presale_assignment_notifications.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, fragment := range []string{
		"CREATE TABLE crm_presale_assignment_events", "UNIQUE KEY uq_presale_assignment_event_id (event_id)",
		"FOREIGN KEY (tenant_id,request_id)", "FOREIGN KEY (tenant_id,assignment_id)",
		"recipient_person_id", "event_type IN ('ADDED','REMOVED')",
		"PRESALE_ASSIGNEE_ADDED", "PRESALE_ASSIGNEE_REMOVED", "ASSIGNEE_ADDED", "ASSIGNEE_REMOVED",
		"idx_crm_notification_request", "chk_crm_notification_resource_shape",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
	if strings.Contains(sql, "INSERT INTO crm_presale_assignment_events") || strings.Contains(sql, "INSERT INTO crm_notifications") {
		t.Fatal("migration must not synthesize assignment history or personal notifications")
	}
	readme, err := os.ReadFile("../../../migrations/README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "29. `000042_presale_assignment_notifications.up.sql`") {
		t.Fatal("CRM migration order does not register 000042 after 000040")
	}
}

func TestAssignmentEventIdentityIsStableAndActionSpecific(t *testing.T) {
	added := AssignmentNotificationEventID("tenant-a", 7, 9, AssignmentEventAdded)
	removed := AssignmentNotificationEventID("tenant-a", 7, 9, AssignmentEventRemoved)
	if len(added) != 64 || added == removed || added == AssignmentNotificationEventID("tenant-b", 7, 9, AssignmentEventAdded) || added == AssignmentNotificationEventID("tenant-a", 7, 10, AssignmentEventAdded) {
		t.Fatalf("added=%q removed=%q", added, removed)
	}
}
