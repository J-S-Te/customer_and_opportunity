package requestaudit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 本测试要求一次性 MySQL 数据库，禁止使用业务数据库（与 hold_test.go 同一约束）。
// 覆盖审计 P1 清理的两个语义：只删"已投递且早于 cutoff"的行，
// 且单次调用最多删 purgeBatchSize 行（分批由 dispatcher 循环完成）。
func TestMySQLPurgeDeliveredIsBatchedAndStatusScoped(t *testing.T) {
	dsn := os.Getenv("REQUESTAUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("REQUESTAUDIT_TEST_DSN must point to a disposable MySQL database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("connect to test database failed")
	}
	var dbName string
	if err := db.Raw("SELECT DATABASE()").Scan(&dbName).Error; err != nil || dbName != "requestaudit_test" {
		t.Fatal("requires requestaudit_test database")
	}
	table := "requestaudit_purge_test"
	if err := db.Table(table).AutoMigrate(&Record{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Error(err)
		}
	})

	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-30 * 24 * time.Hour)
	expired := cutoff.Add(-24 * time.Hour)
	recent := cutoff.Add(24 * time.Hour)
	row := func(eventID, status string, deliveredAt *time.Time) Record {
		return Record{EventID: eventID, TenantID: "tenant", ApplicationCode: "crm", EnvironmentCode: "dev",
			DeliveryStatus: status, Attempts: 1, OccurredAt: now, CreatedAt: now, UpdatedAt: now, DeliveredAt: deliveredAt}
	}
	// 超过一个批次上限的过期 DELIVERED 行：证明单次调用只删一批。
	batch := make([]Record, 0, purgeBatchSize+5)
	for i := 0; i < purgeBatchSize+5; i++ {
		batch = append(batch, row(fmt.Sprintf("expired-%04d", i), StatusDelivered, &expired))
	}
	batch = append(batch,
		row("recent-delivered", StatusDelivered, &recent),
		row("pending", StatusPending, nil),
		row("retry", StatusRetry, nil),
		row("processing", StatusProcessing, nil),
	)
	if err := db.Table(table).CreateInBatches(&batch, 200).Error; err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, tableName: table}
	ctx := context.Background()

	first, err := store.PurgeDelivered(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if first != purgeBatchSize {
		t.Fatalf("第一批删除 %d 行，want %d（单次调用必须受 LIMIT 约束）", first, purgeBatchSize)
	}
	// dispatcher 侧循环：继续分批直到没有可删的行。
	total := first
	for round := 0; round < 20; round++ {
		purged, err := store.PurgeDelivered(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if purged == 0 {
			break
		}
		total += purged
	}
	if total != int64(purgeBatchSize+5) {
		t.Fatalf("累计删除 %d 行，want %d", total, purgeBatchSize+5)
	}
	var survivors []struct {
		EventID        string
		DeliveryStatus string
	}
	if err := db.Table(table).Select("event_id, delivery_status").Order("event_id").Find(&survivors).Error; err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, survivor := range survivors {
		got[survivor.EventID] = survivor.DeliveryStatus
	}
	want := map[string]string{"recent-delivered": StatusDelivered, "pending": StatusPending, "retry": StatusRetry, "processing": StatusProcessing}
	if len(got) != len(want) {
		t.Fatalf("剩余 %d 行，want %d：%+v", len(got), len(want), got)
	}
	for eventID, status := range want {
		if got[eventID] != status {
			t.Fatalf("%s = %q, want %q（未投递/未过期证据不得删除）", eventID, got[eventID], status)
		}
	}
}
