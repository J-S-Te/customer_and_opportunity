package account

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCanUpdatePendingLinkAccountNoOnlyBeforeActivation(t *testing.T) {
	tests := []struct {
		status IdentityStatus
		want   bool
	}{
		{status: IdentityPending, want: true},
		{status: IdentityActive, want: false},
		{status: IdentityDisabled, want: false},
	}
	for _, test := range tests {
		if got := canUpdatePendingLinkAccountNo(test.status); got != test.want {
			t.Fatalf("canUpdatePendingLinkAccountNo(%q) = %t, want %t", test.status, got, test.want)
		}
	}
}

func TestActiveSessionRecheckUsesEveryValidityPredicate(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "portal:test@tcp(127.0.0.1:9910)/portal?parseTime=true&loc=UTC",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	now := time.Date(2026, 8, 3, 8, 9, 10, 123_000_000, time.UTC)
	var active struct {
		SessionIDHash string `gorm:"column:session_id_hash"`
	}
	statement := activeSessionQuery(db, "tenant-a", "session-hash", now).Take(&active).Statement

	sql := statement.SQL.String()
	for _, expected := range []string{
		"portal_sessions",
		"tenant_id = ?",
		"session_id_hash = ?",
		"revoked_at IS NULL",
		"last_seen_at > ?",
		"expires_at > ?",
		"absolute_expiry > ?",
		"deleted_at IS NULL",
		"FOR UPDATE",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("active session recheck SQL missing %q: %s", expected, sql)
		}
	}
	if len(statement.Vars) < 5 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != "session-hash" {
		t.Fatalf("active session recheck variables = %#v", statement.Vars)
	}
	idleCutoff, ok := statement.Vars[2].(time.Time)
	if !ok || !idleCutoff.Equal(now.Add(-portalSessionIdleTimeout)) {
		t.Fatalf("active session idle cutoff = %#v, want %s", statement.Vars[2], now.Add(-portalSessionIdleTimeout))
	}
	for _, value := range statement.Vars[3:5] {
		got, ok := value.(time.Time)
		if !ok || !got.Equal(now) {
			t.Fatalf("active session recheck time variable = %#v, want %s", value, now)
		}
	}
}

func TestTouchSessionRechecksZeroChangedRowsAndFailsClosed(t *testing.T) {
	state := &touchSessionDatabase{active: true}
	repository := NewGORMRepository(openTouchSessionDatabase(t, state))
	now := time.Date(2026, 8, 3, 8, 9, 10, 123_000_000, time.UTC)

	if err := repository.TouchSession(context.Background(), "tenant-a", "session-hash", now, now); err != nil {
		t.Fatalf("TouchSession() rejected active zero-change row: %v", err)
	}
	if state.updates != 1 || state.rechecks != 1 {
		t.Fatalf("TouchSession() calls updates=%d rechecks=%d, want 1/1", state.updates, state.rechecks)
	}

	state.active = false
	if err := repository.TouchSession(context.Background(), "tenant-a", "session-hash", now, now); err != ErrInvalidLoginState {
		t.Fatalf("TouchSession() inactive error = %v, want ErrInvalidLoginState", err)
	}
	if state.updates != 2 || state.rechecks != 2 {
		t.Fatalf("TouchSession() calls updates=%d rechecks=%d, want 2/2", state.updates, state.rechecks)
	}
}

func TestTouchSessionRejectsImpossibleMultiRowUpdate(t *testing.T) {
	state := &touchSessionDatabase{active: true, rowsAffected: 2}
	repository := NewGORMRepository(openTouchSessionDatabase(t, state))
	now := time.Date(2026, 8, 3, 8, 9, 10, 123_000_000, time.UTC)

	if err := repository.TouchSession(context.Background(), "tenant-a", "session-hash", now, now); err != ErrInvalidLoginState {
		t.Fatalf("TouchSession() multi-row error = %v, want ErrInvalidLoginState", err)
	}
	if state.updates != 1 || state.rechecks != 0 {
		t.Fatalf("TouchSession() calls updates=%d rechecks=%d, want 1/0", state.updates, state.rechecks)
	}
}

type touchSessionDatabase struct {
	active       bool
	rowsAffected int64
	updates      int
	rechecks     int
}

func openTouchSessionDatabase(t *testing.T, state *touchSessionDatabase) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(touchSessionConnector{state: state})
	t.Cleanup(func() { _ = sqlDB.Close() })
	database, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open TouchSession test database: %v", err)
	}
	return database
}

type touchSessionConnector struct{ state *touchSessionDatabase }

func (connector touchSessionConnector) Connect(context.Context) (driver.Conn, error) {
	return &touchSessionConnection{state: connector.state}, nil
}
func (connector touchSessionConnector) Driver() driver.Driver {
	return touchSessionDriver{state: connector.state}
}

type touchSessionDriver struct{ state *touchSessionDatabase }

func (value touchSessionDriver) Open(string) (driver.Conn, error) {
	return &touchSessionConnection{state: value.state}, nil
}

type touchSessionConnection struct{ state *touchSessionDatabase }

func (*touchSessionConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*touchSessionConnection) Close() error                        { return nil }
func (*touchSessionConnection) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (connection *touchSessionConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.state.updates++
	return driver.RowsAffected(connection.state.rowsAffected), nil
}
func (connection *touchSessionConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	connection.state.rechecks++
	return &touchSessionRows{active: connection.state.active}, nil
}

type touchSessionRows struct {
	active bool
	done   bool
}

func (*touchSessionRows) Columns() []string { return []string{"session_id_hash"} }
func (*touchSessionRows) Close() error      { return nil }
func (rows *touchSessionRows) Next(destination []driver.Value) error {
	if !rows.active || rows.done {
		return io.EOF
	}
	rows.done = true
	destination[0] = "session-hash"
	return nil
}
