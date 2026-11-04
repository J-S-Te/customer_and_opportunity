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

// record 用途：并发安全地捕获激活写入与清理语句。
// 参数：query 为执行 SQL；args 为绑定的合成参数。
// 返回值：无。
func (state *activationCleanupState) record(query string, args []driver.NamedValue) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.statements = append(state.statements, query)
	state.variables = append(state.variables, append([]driver.NamedValue(nil), args...))
}

// snapshot 用途：返回已捕获激活语句及参数的独立切片。
// 参数：无。
// 返回值：语句及其绑定参数快照。
func (state *activationCleanupState) snapshot() ([]string, [][]driver.NamedValue) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.statements...), append([][]driver.NamedValue(nil), state.variables...)
}

type activationCleanupConnector struct{ state *activationCleanupState }

// Connect 用途：为激活清理测试创建内存驱动连接。
// 参数：忽略连接上下文；不访问外部数据库。
// 返回值：测试连接及 nil 错误。
func (connector activationCleanupConnector) Connect(context.Context) (driver.Conn, error) {
	return &activationCleanupConnection{state: connector.state}, nil
}

// Driver 用途：返回共享激活语句记录器的测试驱动。
// 参数：无。
// 返回值：内存测试驱动。
func (connector activationCleanupConnector) Driver() driver.Driver {
	return activationCleanupDriver{state: connector.state}
}

type activationCleanupDriver struct{ state *activationCleanupState }

// Open 用途：打开不访问网络的激活清理测试连接。
// 参数：忽略合成连接串。
// 返回值：测试连接及 nil 错误。
func (value activationCleanupDriver) Open(string) (driver.Conn, error) {
	return &activationCleanupConnection{state: value.state}, nil
}

type activationCleanupConnection struct{ state *activationCleanupState }

// Prepare 用途：拒绝预处理以要求测试走直接执行接口。
// 参数：忽略 SQL 文本。
// 返回值：无语句；返回 driver.ErrSkip。
func (*activationCleanupConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }

// Close 用途：结束无外部资源的测试连接或结果集。
// 参数：无。
// 返回值：nil，表示测试资源关闭成功。
func (*activationCleanupConnection) Close() error { return nil }

// Begin 用途：拒绝测试中未启用的默认事务路径。
// 参数：无。
// 返回值：无事务；返回 driver.ErrSkip。
func (*activationCleanupConnection) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

// ExecContext 用途：记录激活语句并按夹具模拟清理依赖失败。
// 参数：忽略上下文；query 为 SQL；args 为合成绑定参数。
// 返回值：测试写入结果；配置的清理失败返回错误。
func (connection *activationCleanupConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	connection.state.record(query, args)
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "DELETE") && connection.state.cleanupFail {
		return nil, errors.New("portal_activation_contexts unavailable")
	}
	return activationCleanupResult{}, nil
}

// QueryContext 用途：为激活清理测试返回空结果集。
// 参数：忽略请求上下文、SQL 与合成参数。
// 返回值：空结果集及 nil 错误。
func (*activationCleanupConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &emptyRows{}, nil
}

// activationCleanupResult 让 GORM 的 Create 能读到自增 ID，否则会因驱动不支持
// LastInsertId 而误判写入失败。
type activationCleanupResult struct{}

// LastInsertId 用途：为激活插入提供测试自增标识。
// 参数：无。
// 返回值：固定合成标识 1 及 nil 错误。
func (activationCleanupResult) LastInsertId() (int64, error) { return 1, nil }

// RowsAffected 用途：为激活写入提供测试影响行数。
// 参数：无。
// 返回值：一行及 nil 错误。
func (activationCleanupResult) RowsAffected() (int64, error) { return 1, nil }

type emptyRows struct{}

// Columns 用途：描述测试空结果集的列。
// 参数：无。
// 返回值：仅包含 id 的列清单。
func (*emptyRows) Columns() []string { return []string{"id"} }

// Close 用途：结束无外部资源的测试连接或结果集。
// 参数：无。
// 返回值：nil，表示测试资源关闭成功。
func (*emptyRows) Close() error { return nil }

// Next 用途：结束测试空结果集的迭代。
// 参数：忽略结果行接收缓冲区。
// 返回值：io.EOF，表示没有更多测试行。
func (*emptyRows) Next([]driver.Value) error { return io.EOF }

// openActivationCleanupDatabase 用途：将内存驱动装配为激活清理测试数据库。
// 参数：t 为测试上下文；state 为语句与错误夹具。
// 返回值：测试数据库；装配失败时终止测试。
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

// TestCreateActivationRunsBoundedExpiryCleanup 用途：验证登录上下文写入后按当前时间有界清理过期行。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// TestCreateActivationIgnoresCleanupFailure 用途：验证机会式清理失败不阻止已建立的登录事务。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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
