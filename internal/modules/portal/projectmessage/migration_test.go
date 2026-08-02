package projectmessage

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationEnforcesRecipientAndIdempotencyBindings(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000050_portal_project_messages.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, expected := range []string{
		"manager_portal_account_id", "collate utf8mb4_0900_bin", "uq_portal_project_conversation",
		"uq_portal_project_conversation_create", "uq_portal_project_message_key",
		"idx_portal_project_message_rate", "fk_portal_project_message_conversation",
		"fk_portal_project_message_event_message",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestKeysetReadMigrationUsesTenantBoundPerMessageReceipts(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000054_portal_project_message_keyset_reads.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, expected := range []string{"portal_project_message_reads", "uq_portal_project_message_read", "foreign key (tenant_id,conversation_id,message_id)", "recipient_account_id=r.reader_account_id"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestReadReceiptMigrationEnforcesConversationAndMessageBindings(t *testing.T) {
	raw, err := os.ReadFile("../../../../migrations/000052_portal_project_message_read_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	for _, expected := range []string{"uq_portal_project_message_receipt", "uq_portal_project_message_cursor", "fk_portal_project_message_receipt_conversation", "fk_portal_project_message_receipt_message", "reader_account_id", "last_read_message_id", "last_read_cursor"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}
