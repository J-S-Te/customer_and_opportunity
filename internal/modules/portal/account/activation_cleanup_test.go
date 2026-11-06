package account

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// activationCleanupState 记录 CreateActivation 发出的语句，使清理语句的形状与失败容忍
// 都能在没有真实 MySQL 的情况下断言。
type activationCleanupState struct {
	mu          sync.Mutex
	statements  []string
	variables   [][]driver.NamedValue
	cleanupFail bool
}

func (state *activationCleanupState) record(query string, args []driver.NamedValue) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.statements = append(state.statements, query)
	state.variables = append(state.variables, append([]driver.NamedValue(nil), args...))
}

func (state *activationCleanupState) snapshot() ([]string, [][]driver.NamedValue) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.statements...), append([][]driver.NamedValue(nil), state.variables...)
}

type activationCleanupConnector struct{ state *activationCleanupState }

func (connector activationCleanupConnector) Connect(context.Context) (driver.Conn, error) {
	return &activationCleanupConnection{state: connector.state}, nil
}
func (connector activationCleanupConnector) Driver() driver.Driver {
	return activationCleanupDriver{state: connector.state}
}

type activationCleanupDriver struct{ state *activationCleanupState }

func (value activationCleanupDriver) Open(string) (driver.Conn, error) {
	return &activationCleanupConnection{state: value.state}, nil
}

type activationCleanupConnection struct{ state *activationCleanupState }

func (*activationCleanupConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*activationCleanupConnection) Close() error                        { return nil }
func (*activationCleanupConnection) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (connection *activationCleanupConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	connection.state.record(query, args)
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "DELETE") && connection.state.cleanupFail {
		return nil, errors.New("portal_activation_contexts unavailable")
	}
	return activationCleanupResult{}, nil
}
func (*activationCleanupConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &emptyRows{}, nil
}

// activationCleanupResult 让 GORM 的 Create 能读到自增 ID，否则会因驱动不支持
// LastInsertId 而误判写入失败。
type activationCleanupResult struct{}

func (activationCleanupResult) LastInsertId() (int64, error) { return 1, nil }
func (activationCleanupResult) RowsAffected() (int64, error) { return 1, nil }

type emptyRows struct{}

func (*emptyRows) Columns() []string         { return []string{"id"} }
func (*emptyRows) Close() error              { return nil }
func (*emptyRows) Next([]driver.Value) error { return io.EOF }

func openActivationCleanupDatabase(t *testing.T, state *activationCleanupState) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(activationCleanupConnector{state: state})
	t.Cleanup(func() { _ = sqlDB.Close() })
	database, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open CreateActivation test database: %v", err)
	}
	return database
}

func TestCreateActivationRunsBoundedExpiryCleanup(t *testing.T) {
	state := &activationCleanupState{}
	repository := NewGORMRepository(openActivationCleanupDatabase(t, state))
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	activation := &ActivationContext{
		Model: newModel("tenant-a", "portal-login", now), ContextIDHash: "context-hash", StateHash: "state-hash",
		NonceHash: "nonce-hash", NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"),
		ReturnPath: "/", ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := repository.CreateActivation(context.Background(), activation); err != nil {
		t.Fatalf("CreateActivation() error = %v", err)
	}
	statements, variables := state.snapshot()
	if len(statements) != 2 {
		t.Fatalf("CreateActivation emitted %d statements, want insert + expiry cleanup: %#v", len(statements), statements)
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statements[0])), "INSERT") {
		t.Fatalf("first statement must be the activation insert: %s", statements[0])
	}
	// 清理必须无租户条件（跨租户收敛历史行）、按过期时间有界删除。
	cleanup := statements[1]
	for _, expected := range []string{"DELETE", "portal_activation_contexts", "expires_at", "ORDER BY", "LIMIT"} {
		if !strings.Contains(cleanup, expected) {
			t.Fatalf("cleanup statement missing %q: %s", expected, cleanup)
		}
	}
	if strings.Contains(cleanup, "tenant_id") {
		t.Fatalf("cleanup must not be tenant-scoped: %s", cleanup)
	}
	args := variables[1]
	if len(args) != 2 {
		t.Fatalf("cleanup arguments = %#v", args)
	}
	cutoff, ok := args[0].Value.(time.Time)
	if !ok {
		t.Fatalf("cleanup cutoff type = %T", args[0].Value)
	}
	// 以当前时间为界：不能删掉尚未过期的待登录上下文，否则会把正在登录的用户踢回起点。
	if !cutoff.Before(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("cleanup cutoff = %s, want current time", cutoff)
	}
	if batch, ok := args[1].Value.(int64); !ok || batch != activationCleanupBatch {
		t.Fatalf("cleanup batch = %#v, want %d", args[1].Value, activationCleanupBatch)
	}
}

func TestCreateActivationIgnoresCleanupFailure(t *testing.T) {
	state := &activationCleanupState{cleanupFail: true}
	repository := NewGORMRepository(openActivationCleanupDatabase(t, state))
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	activation := &ActivationContext{
		Model: newModel("tenant-a", "portal-login", now), ContextIDHash: "context-hash", StateHash: "state-hash",
		NonceHash: "nonce-hash", NonceCipher: []byte("nonce"), PKCEVerifierCipher: []byte("verifier"),
		ReturnPath: "/", ExpiresAt: now.Add(10 * time.Minute),
	}
	// 清理只做机会式收敛：失败不能把已经写入的 activation 变成登录失败。
	if err := repository.CreateActivation(context.Background(), activation); err != nil {
		t.Fatalf("cleanup failure blocked activation creation: %v", err)
	}
	statements, _ := state.snapshot()
	if len(statements) != 2 {
		t.Fatalf("statements = %#v", statements)
	}
}
