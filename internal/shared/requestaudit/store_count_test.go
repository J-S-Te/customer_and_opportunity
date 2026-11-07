package requestaudit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// requestAuditDryRunDB 不连业务库，只用 GORM DryRun 断言聚合计数查询的语句形状。
func requestAuditDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	return db
}

// captureCount 在 DryRun 下调用真实存储方法，捕获它实际生成的聚合计数语句。
func captureCount(t *testing.T, call func(*Store) error) (string, []any) {
	t.Helper()
	db := requestAuditDryRunDB(t)
	var sqlText string
	var vars []any
	if err := db.Callback().Query().After("gorm:query").Register("test:capture_count", func(tx *gorm.DB) {
		sqlText = tx.Statement.SQL.String()
		vars = append([]any(nil), tx.Statement.Vars...)
	}); err != nil {
		t.Fatalf("register statement capture: %v", err)
	}
	store := &Store{db: db, tableName: crmTableName}
	if err := call(store); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if sqlText == "" {
		t.Fatal("count query produced no statement")
	}
	return sqlText, vars
}

// PendingCount 是 /readyz 的积压口径：PENDING、RETRY、PROCESSING 都算尚未投递完成，
// 只有与 DA/PM 口径一致，投递故障（事件进入 RETRY 退避或卡在 PROCESSING）才不会被报成 0。
func TestPendingCountAggregatesPendingRetryAndProcessing(t *testing.T) {
	sql, vars := captureCount(t, func(store *Store) error {
		_, err := store.PendingCount(context.Background())
		return err
	})
	if !strings.Contains(sql, crmTableName) {
		t.Fatalf("pending query does not target the outbox table: %q", sql)
	}
	if !strings.Contains(sql, "delivery_status IN") {
		t.Fatalf("pending query must aggregate several delivery statuses: %q", sql)
	}
	want := []string{StatusPending, StatusRetry, StatusProcessing}
	if len(vars) != len(want) {
		t.Fatalf("vars=%#v want the three unsettled statuses", vars)
	}
	for index, status := range want {
		if vars[index] != status {
			t.Fatalf("vars=%#v want %#v", vars, want)
		}
	}
	for _, excluded := range []string{StatusDelivered, StatusStarted} {
		for _, value := range vars {
			if value == excluded {
				t.Fatalf("pending query must not count %s: vars=%#v", excluded, vars)
			}
		}
	}
}

// HeldCount 只统计被 HoldSourceMismatch 挂起的人工介入行：
// RETRY 且 next_attempt_at 为 NULL（Claim 永不认领），单列计数才不会被 RETRY 积压淹没。
func TestHeldCountCountsOnlyRetryWithoutNextAttempt(t *testing.T) {
	sql, vars := captureCount(t, func(store *Store) error {
		_, err := store.HeldCount(context.Background())
		return err
	})
	if !strings.Contains(sql, crmTableName) {
		t.Fatalf("held query does not target the outbox table: %q", sql)
	}
	if !strings.Contains(sql, "next_attempt_at IS NULL") {
		t.Fatalf("held query must select rows with no next attempt: %q", sql)
	}
	if !strings.Contains(sql, "delivery_status") {
		t.Fatalf("held query must restrict the delivery status: %q", sql)
	}
	if len(vars) != 1 || vars[0] != StatusRetry {
		t.Fatalf("vars=%#v want only the RETRY status", vars)
	}
}

// 本测试要求一次性 MySQL 数据库，禁止使用业务数据库。
// 它验证真实行集口径：PENDING/RETRY/PROCESSING 都计入积压，DELIVERED/STARTED 不计；
// 挂起行（RETRY 且 next_attempt_at 为 NULL）单独计入 HeldCount。
func TestMySQLPendingAndHeldCountSemantics(t *testing.T) {
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
	table := "requestaudit_count_test"
	if err := db.Table(table).AutoMigrate(&Record{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	store := &Store{db: db, tableName: table}
	now := time.Now().UTC().Truncate(time.Second)
	later := now.Add(time.Minute)
	rows := []Record{
		{EventID: "pending", TenantID: "tenant", DeliveryStatus: StatusPending, NextAttemptAt: &now, OccurredAt: now, CreatedAt: now, UpdatedAt: now},
		{EventID: "retry", TenantID: "tenant", DeliveryStatus: StatusRetry, NextAttemptAt: &later, OccurredAt: now, CreatedAt: now, UpdatedAt: now},
		{EventID: "processing", TenantID: "tenant", DeliveryStatus: StatusProcessing, NextAttemptAt: &now, LockedBy: "worker", LockedUntil: &later, OccurredAt: now, CreatedAt: now, UpdatedAt: now},
		{EventID: "held", TenantID: "tenant", DeliveryStatus: StatusRetry, NextAttemptAt: nil, LastErrorCode: "PLATFORM_AUDIT_OUTBOX_SOURCE_MISMATCH", OccurredAt: now, CreatedAt: now, UpdatedAt: now},
		{EventID: "delivered", TenantID: "tenant", DeliveryStatus: StatusDelivered, DeliveredAt: &now, OccurredAt: now, CreatedAt: now, UpdatedAt: now},
		{EventID: "started", TenantID: "tenant", DeliveryStatus: StatusStarted, OccurredAt: now, CreatedAt: now, UpdatedAt: now},
	}
	for index := range rows {
		if err := db.Table(table).Create(&rows[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.PendingCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 4 {
		t.Fatalf("pending=%d want 4 (PENDING+RETRY+PROCESSING+held RETRY)", pending)
	}
	held, err := store.HeldCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Fatalf("held=%d want 1 (RETRY without next_attempt_at)", held)
	}
}
