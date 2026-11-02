package handoverguard

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlRecorder 收集 GORM 生成的 SQL，用于在无数据库的 DryRun 下断言门闩语句形状。
type sqlRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (recorder *sqlRecorder) Printf(format string, args ...any) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.lines = append(recorder.lines, fmt.Sprintf(format, args...))
}

func (recorder *sqlRecorder) String() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return strings.Join(recorder.lines, "\n")
}

func dryRunDB(t *testing.T) (*gorm.DB, *sqlRecorder) {
	t.Helper()
	recorder := &sqlRecorder{}
	db, err := gorm.Open(
		mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.New(recorder, logger.Config{LogLevel: logger.Info})},
	)
	if err != nil {
		t.Fatal(err)
	}
	return db, recorder
}

// 门闩必须严格按「保证行存在 → 锁行 → 同事务查 FROZEN」的顺序生成语句。
func TestGuardEmitsGateLockBeforeFreezeRecheck(t *testing.T) {
	db, recorder := dryRunDB(t)
	_ = Guard(db, "tenant-1", "user-1")
	sql := recorder.String()
	insertAt := strings.Index(sql, "INSERT INTO crm_handover_gate")
	lockAt := strings.Index(sql, "SELECT * FROM `crm_handover_gate` WHERE tenant_id = 'tenant-1'")
	freezeAt := strings.Index(sql, "FROM `crm_handover_freeze`")
	if insertAt < 0 || lockAt < 0 || freezeAt < 0 {
		t.Fatalf("guard SQL is incomplete: %s", sql)
	}
	if !strings.Contains(sql, "FOR UPDATE") {
		t.Fatalf("gate row is not locked: %s", sql)
	}
	if !strings.Contains(sql, "ON DUPLICATE KEY UPDATE updated_at=updated_at") && !strings.Contains(sql, "ON DUPLICATE KEY UPDATE updated_at = updated_at") {
		t.Fatalf("gate row upsert is not idempotent: %s", sql)
	}
	if !(insertAt < lockAt && lockAt < freezeAt) {
		t.Fatalf("guard statements are out of order: insert=%d lock=%d freeze=%d\n%s", insertAt, lockAt, freezeAt, sql)
	}
	if !strings.Contains(sql, "status = 'FROZEN'") {
		t.Fatalf("freeze recheck does not restrict to FROZEN: %s", sql)
	}
	// 复核必须是加锁读：普通 SELECT 会读到写事务更早建立的快照而漏判。
	freezeClause := sql[strings.Index(sql, "FROM `crm_handover_freeze`"):]
	if !strings.Contains(freezeClause, "FOR UPDATE") {
		t.Fatalf("freeze recheck must be a locking read: %s", sql)
	}
}

// 事务外调用不构成串行化，必须直接拒绝而不是静默降级。
func TestGuardRejectsMissingTransactionAndSubject(t *testing.T) {
	if err := Guard(nil, "tenant-1", "user-1"); !errors.Is(err, ErrTransactionRequired) {
		t.Fatalf("nil transaction err = %v, want ErrTransactionRequired", err)
	}
	db, _ := dryRunDB(t)
	if err := Guard(db, "  ", "user-1"); !errors.Is(err, ErrSubjectRequired) {
		t.Fatalf("missing tenant err = %v, want ErrSubjectRequired", err)
	}
	if err := Lock(db, "tenant-1", "  "); !errors.Is(err, ErrSubjectRequired) {
		t.Fatalf("missing user err = %v, want ErrSubjectRequired", err)
	}
}

// 一次性 MySQL schema：未配置 HANDOVER_TEST_MYSQL_DSN 时跳过（与交接仓储测试同一门控）。
func mysqlGateDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("HANDOVER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("HANDOVER_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open disposable MySQL schema: %v", err)
	}
	models := []any{&gateRow{}, &testFreezeRow{}}
	for _, model := range models {
		_ = db.Migrator().DropTable(model)
	}
	t.Cleanup(func() {
		for _, model := range models {
			_ = db.Migrator().DropTable(model)
		}
	})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}
	return db
}

type testFreezeRow struct {
	ID       string `gorm:"column:id;size:26;primaryKey"`
	TenantID string `gorm:"column:tenant_id;size:64"`
	UserID   string `gorm:"column:user_id;size:64"`
	Status   string `gorm:"column:status;size:16"`
}

func (testFreezeRow) TableName() string { return "crm_handover_freeze" }

// 门闩行自愈：首次调用插入行，随后锁行并在同一事务内读到冻结即拒绝写入。
func TestGuardSerializesFreezeAndWritesOnMySQL(t *testing.T) {
	db := mysqlGateDB(t)
	if err := db.Transaction(func(tx *gorm.DB) error { return Guard(tx, "tenant-1", "user-1") }); err != nil {
		t.Fatalf("unfrozen user must pass the guard: %v", err)
	}
	var gates int64
	if err := db.Model(&gateRow{}).Where("tenant_id = ? AND user_id = ?", "tenant-1", "user-1").Count(&gates).Error; err != nil || gates != 1 {
		t.Fatalf("gate row count = %d err = %v, want 1", gates, err)
	}
	if err := db.Create(&testFreezeRow{ID: "01FREEZE000000000000000001", TenantID: "tenant-1", UserID: "user-1", Status: "FROZEN"}).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error { return Guard(tx, "tenant-1", "user-1") })
	if !errors.Is(err, ErrUserFrozen) {
		t.Fatalf("frozen user err = %v, want ErrUserFrozen", err)
	}
	// 已释放的冻结不再阻塞责任写入。
	if err := db.Model(&testFreezeRow{}).Where("tenant_id = ? AND user_id = ?", "tenant-1", "user-1").Update("status", "RELEASED").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return Guard(tx, "tenant-1", "user-1") }); err != nil {
		t.Fatalf("released freeze must not block writes: %v", err)
	}
}

// 写事务已有更早的快照时，冻结复核仍必须看到刚刚提交的冻结行（加锁读语义）。
func TestGuardSeesFreezeCommittedAfterTransactionSnapshot(t *testing.T) {
	db := mysqlGateDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer func() { _ = tx.Rollback() }()
	var snapshotCount int64
	if err := tx.Table("crm_handover_freeze").Where("tenant_id = ?", "tenant-1").Count(&snapshotCount).Error; err != nil {
		t.Fatalf("establish snapshot: %v", err)
	}
	// 另一条连接在快照建立之后提交冻结。
	if err := db.Create(&testFreezeRow{ID: "01FREEZE000000000000000002", TenantID: "tenant-1", UserID: "user-2", Status: "FROZEN"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Guard(tx, "tenant-1", "user-2"); !errors.Is(err, ErrUserFrozen) {
		t.Fatalf("guard err = %v, want ErrUserFrozen despite the older snapshot", err)
	}
}

// 门闩行锁把"持有者事务"与"另一个写入事务"串行化：后者必须等待前者结束。
func TestGuardBlocksConcurrentWriterUntilHolderCommits(t *testing.T) {
	db := mysqlGateDB(t)
	holder := db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	if err := Guard(holder, "tenant-1", "user-1"); err != nil {
		t.Fatalf("holder guard: %v", err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- db.Transaction(func(tx *gorm.DB) error { return Guard(tx, "tenant-1", "user-1") })
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("concurrent writer must wait for the gate row lock, got err = %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := holder.Rollback().Error; err != nil {
		t.Fatalf("rollback holder: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent writer after holder rollback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent writer did not resume after the holder released the gate row")
	}
}
