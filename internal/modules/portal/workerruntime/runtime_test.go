package workerruntime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestRepositoryFailsClosedWithoutDatabase(t *testing.T) {
	repo := NewRepository(nil)
	now := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	if ready, err := repo.HasFreshHeartbeat(context.Background(), ReportDeliveryWorker, now.Add(-HeartbeatMaxAge)); err == nil || ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	if err := repo.Start(context.Background(), ReportDeliveryWorker, "instance", now); err == nil {
		t.Fatal("nil database must fail closed")
	}
	if err := repo.Refresh(context.Background(), ReportDeliveryWorker, "instance", now, now); err == nil {
		t.Fatal("nil database must fail closed")
	}
	if err := repo.Remove(context.Background(), ReportDeliveryWorker, "instance", now); err == nil {
		t.Fatal("nil database must fail closed")
	}
}

func TestFencingSQLBindsIncarnationAndStartReplacesIt(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: dryRunConn{}, SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var createdSQL, refreshedSQL, removedSQL string
	_ = db.Callback().Create().After("gorm:create").Register("test:capture-start", func(tx *gorm.DB) { createdSQL = tx.Statement.SQL.String() })
	_ = db.Callback().Update().After("gorm:update").Register("test:capture-refresh", func(tx *gorm.DB) { refreshedSQL = tx.Statement.SQL.String() })
	_ = db.Callback().Delete().After("gorm:delete").Register("test:capture-remove", func(tx *gorm.DB) { removedSQL = tx.Statement.SQL.String() })
	repo := NewRepository(db)
	oldStartedAt := time.Date(2026, 8, 2, 1, 2, 3, 987654321, time.UTC)
	if err = repo.Start(context.Background(), ReportDeliveryWorker, "shared-id", oldStartedAt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(createdSQL, "ON DUPLICATE KEY UPDATE") || !strings.Contains(createdSQL, "started_at") {
		t.Fatalf("start SQL does not replace incarnation: %s", createdSQL)
	}
	if err = repo.Refresh(context.Background(), ReportDeliveryWorker, "shared-id", oldStartedAt, oldStartedAt.Add(time.Second)); !errors.Is(err, ErrIncarnationLost) {
		t.Fatalf("dry-run refresh err=%v", err)
	}
	if !strings.Contains(refreshedSQL, "started_at") {
		t.Fatalf("refresh SQL lacks incarnation fence: %s", refreshedSQL)
	}
	if err = repo.Remove(context.Background(), ReportDeliveryWorker, "shared-id", oldStartedAt); !errors.Is(err, ErrIncarnationLost) {
		t.Fatalf("dry-run remove err=%v", err)
	}
	if !strings.Contains(removedSQL, "started_at") {
		t.Fatalf("remove SQL lacks incarnation fence: %s", removedSQL)
	}
}

// dryRunConn is never called because GORM DryRun renders statements only.
type dryRunConn struct{}

func (dryRunConn) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("unexpected database call")
}
func (dryRunConn) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	panic("unexpected database call")
}
func (dryRunConn) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	panic("unexpected database call")
}
func (dryRunConn) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	panic("unexpected database call")
}

func TestRepositoryRejectsInvalidIdentity(t *testing.T) {
	repo := NewRepository(nil)
	now := time.Now().UTC()
	for _, instanceID := range []string{"", strings.Repeat("x", 129)} {
		if err := repo.Start(context.Background(), ReportDeliveryWorker, instanceID, now); err == nil {
			t.Fatalf("instance %q accepted", instanceID)
		}
	}
	if HeartbeatInterval >= HeartbeatMaxAge {
		t.Fatalf("heartbeat interval %s must be below max age %s", HeartbeatInterval, HeartbeatMaxAge)
	}
}

func TestStartedAtIsCanonicalizedToDatabasePrecision(t *testing.T) {
	value := time.Date(2026, 8, 2, 1, 2, 3, 987654321, time.FixedZone("offset", 8*60*60))
	got := canonicalStartedAt(value)
	if got.Location() != time.UTC || got.Nanosecond() != 987000000 || !got.Equal(value.Truncate(time.Millisecond)) {
		t.Fatalf("canonical started_at=%s nanos=%d", got, got.Nanosecond())
	}
}

func TestMySQLFencingPreventsOldIncarnationRefreshAndRemoval(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTAL_WORKER_RUNTIME_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("PORTAL_WORKER_RUNTIME_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE IF NOT EXISTS portal_worker_heartbeats (
id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,worker_type VARCHAR(64) NOT NULL,instance_id VARCHAR(128) NOT NULL,
started_at DATETIME(3) NOT NULL,last_seen_at DATETIME(3) NOT NULL,created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),PRIMARY KEY(id),
UNIQUE KEY uq_portal_worker_heartbeat(worker_type,instance_id),KEY idx_portal_worker_heartbeat_freshness(worker_type,last_seen_at))`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM portal_worker_heartbeats WHERE worker_type=? AND instance_id=?", ReportDeliveryWorker, "fencing-test").Error
	})
	repo := NewRepository(db)
	oldStartedAt := time.Date(2026, 8, 2, 1, 2, 3, 123456789, time.UTC)
	newStartedAt := oldStartedAt.Add(time.Second)
	if err = repo.Start(context.Background(), ReportDeliveryWorker, "fencing-test", oldStartedAt); err != nil {
		t.Fatal(err)
	}
	if err = repo.Start(context.Background(), ReportDeliveryWorker, "fencing-test", newStartedAt); err != nil {
		t.Fatal(err)
	}
	if err = repo.Refresh(context.Background(), ReportDeliveryWorker, "fencing-test", oldStartedAt, newStartedAt.Add(time.Second)); !errors.Is(err, ErrIncarnationLost) {
		t.Fatalf("old refresh err=%v", err)
	}
	if err = repo.Remove(context.Background(), ReportDeliveryWorker, "fencing-test", oldStartedAt); !errors.Is(err, ErrIncarnationLost) {
		t.Fatalf("old remove err=%v", err)
	}
	if err = repo.Refresh(context.Background(), ReportDeliveryWorker, "fencing-test", newStartedAt, newStartedAt.Add(2*time.Second)); err != nil {
		t.Fatalf("new refresh err=%v", err)
	}
	if err = repo.Remove(context.Background(), ReportDeliveryWorker, "fencing-test", newStartedAt); err != nil {
		t.Fatalf("new remove err=%v", err)
	}
}
