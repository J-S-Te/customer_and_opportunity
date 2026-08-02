package report

import (
	"os"
	"strings"
	"testing"
)

func TestIssuedNotificationMigrationIsAccountScopedAndAudited(t *testing.T) {
	up, err := os.ReadFile("../../../../migrations/000043_portal_report_issued_notifications.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE TABLE portal_report_notifications",
		"UNIQUE KEY uq_portal_report_notification_scope",
		"(tenant_id,customer_id,id,request_id,account_id)",
		"(tenant_id,customer_id,request_id,account_id,kind)",
		"(tenant_id,customer_id,account_id,status,created_at,id)",
		"FOREIGN KEY (tenant_id,customer_id,request_id)",
		"REFERENCES portal_report_requests (tenant_id,customer_id,id)",
		"CREATE TABLE portal_report_notification_read_events",
		"UNIQUE KEY uq_portal_report_notification_first_read",
		"FOREIGN KEY (tenant_id,customer_id,notification_id,request_id,account_id)",
		"(tenant_id,customer_id,id,request_id,account_id)",
		"No historical notifications are synthesized",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("issued notification migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(text), "INSERT INTO PORTAL_REPORT_NOTIFICATIONS") {
		t.Fatal("migration must not synthesize notification history")
	}
}

func TestIssuedNotificationDownMigrationWarnsBeforeAuditLoss(t *testing.T) {
	down, err := os.ReadFile("../../../../migrations/000043_portal_report_issued_notifications.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(down)
	if !strings.Contains(text, "append-only read evidence") || !strings.Contains(text, "forward migration") {
		t.Fatalf("down migration lacks controlled rollback warning: %s", text)
	}
}

func TestIssuedNotificationMigrationIsRegisteredAfterPortalActorMigration(t *testing.T) {
	readme, err := os.ReadFile("../../../../migrations/README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	actor := strings.Index(text, "13. `000041_portal_report_actor_columns.up.sql`")
	notice := strings.Index(text, "14. `000043_portal_report_issued_notifications.up.sql`")
	if actor < 0 || notice <= actor {
		t.Fatal("Portal migration order must register 000043 after 000041")
	}
}
