package contracttransferworker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestAuthoritativeCommandAllowsLaterMasterDataVersion(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CONTRACT_TRANSFER_TEST_DSN"))
	if dsn == "" {
		t.Skip("CONTRACT_TRANSFER_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_opportunities (
		id BIGINT UNSIGNED PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
		version BIGINT UNSIGNED NOT NULL, opportunity_no VARCHAR(64) NOT NULL, current_stage VARCHAR(32) NOT NULL,
		opp_status VARCHAR(32) NOT NULL, expected_amount DECIMAL(18,2) NOT NULL, owner_user_id VARCHAR(64) NOT NULL,
		contract_ref VARCHAR(64) NULL, terminal_pending_type VARCHAR(32) NOT NULL, deleted_at DATETIME(3) NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_outbox_events (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, event_id VARCHAR(64) NOT NULL, tenant_id VARCHAR(64) NOT NULL,
		event_type VARCHAR(64) NOT NULL, aggregate_type VARCHAR(64) NOT NULL, aggregate_id VARCHAR(64) NOT NULL,
		payload JSON NOT NULL, status VARCHAR(16) NOT NULL, retry_count INT NOT NULL DEFAULT 0,
		next_retry_at DATETIME(3) NULL, locked_by VARCHAR(128) NOT NULL DEFAULT '', locked_until DATETIME(3) NULL,
		last_error_summary VARCHAR(1000) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, sent_at DATETIME(3) NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		t.Fatal(err)
	}
	const tenant, oppID, token = "tenant-transfer-version", uint64(7), "claim-1"
	now := time.Now().UTC()
	if err = db.Exec(`DELETE FROM crm_opportunities WHERE tenant_id=? AND id=?`, tenant, oppID).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`DELETE FROM crm_outbox_events WHERE tenant_id=?`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`INSERT INTO crm_opportunities(id,tenant_id,customer_id,version,opportunity_no,current_stage,opp_status,expected_amount,owner_user_id,contract_ref,terminal_pending_type)
		VALUES(?,?,?,8,'SJ-7','已签约','CLOSED',100.00,'owner','HT-7','NONE') ON DUPLICATE KEY UPDATE version=8`, oppID, tenant, 42).Error; err != nil {
		t.Fatal(err)
	}
	eventVersion := uint64(7)
	eventID := stableEventID(tenant, oppID, eventVersion)
	payload, _ := json.Marshal(signedPayload{OpportunityID: oppID, EventVersion: eventVersion, CustomerID: 42, ContractRef: "HT-7", ExpectedAmount: "100.00", OccurredAt: now})
	if err = db.Exec(`INSERT INTO crm_outbox_events(event_id,tenant_id,event_type,aggregate_type,aggregate_id,payload,status,locked_by,locked_until,created_at)
		VALUES(?,?,?,?,?,?,'PROCESSING',?,?,?)`, eventID, tenant, eventType, "opportunity", "7", payload, token, now.Add(time.Minute), now).Error; err != nil {
		t.Fatal(err)
	}
	var eventPK uint64
	if err = db.Raw(`SELECT id FROM crm_outbox_events WHERE tenant_id=? AND event_id=?`, tenant, eventID).Scan(&eventPK).Error; err != nil || eventPK == 0 {
		t.Fatalf("find outbox event id=%d err=%v", eventPK, err)
	}
	lockedUntil := now.Add(time.Minute)
	event := opportunity.OutboxEvent{ID: eventPK, EventID: eventID, TenantID: tenant, EventType: eventType, AggregateType: "opportunity", AggregateID: "7", Payload: payload, Status: "PROCESSING", LockedBy: token, LockedUntil: &lockedUntil}
	store := &gormTransferStore{db: db}
	command, reason, err := store.authoritativeCommand(context.Background(), event, now, token)
	if err != nil || reason != "" || command.EventVersion != 7 || command.OpportunityID != 7 {
		t.Fatalf("command=%+v reason=%q err=%v", command, reason, err)
	}
	if err = db.Exec(`UPDATE crm_opportunities SET version=6 WHERE id=?`, oppID).Error; err != nil {
		t.Fatal(err)
	}
	if _, reason, err = store.authoritativeCommand(context.Background(), event, now, token); err != nil || reason == "" {
		t.Fatalf("stale state reason=%q err=%v, want permanent rejection", reason, err)
	}
}
