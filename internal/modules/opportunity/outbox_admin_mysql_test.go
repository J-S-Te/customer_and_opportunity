package opportunity

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestContractTransferOutboxFailedQueryAndRetryOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "qa-crm-outbox-" + time.Now().UTC().Format("150405000000")
	now := time.Now().UTC()
	event := &OutboxEvent{EventID: "evt-" + tenant, TenantID: tenant, EventType: "OPPORTUNITY_SIGNED", AggregateType: "opportunity", AggregateID: "7", Payload: []byte(`{}`), Status: "DEAD_LETTER", RetryCount: 6, LastErrorSummary: "CRM authority read failed", CreatedAt: now}
	if err := db.Create(event).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("tenant_id=?", tenant).Delete(&OutboxEvent{}).Error })
	repo := NewGORMRepository(db)
	items, err := repo.ListFailedContractTransfers(context.Background(), tenant, 100)
	if err != nil || len(items) != 1 || items[0].LastErrorSummary != "CRM authority read failed" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	ok, err := repo.RetryFailedContractTransfer(context.Background(), tenant, event.ID)
	if err != nil || !ok {
		t.Fatalf("retry ok=%v err=%v", ok, err)
	}
	var status string
	var retries uint8
	if err := db.Raw(`SELECT status,retry_count FROM crm_outbox_events WHERE tenant_id=? AND id=?`, tenant, event.ID).Row().Scan(&status, &retries); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || retries != 0 {
		t.Fatalf("status=%s retries=%d, want PENDING/0", status, retries)
	}
}
