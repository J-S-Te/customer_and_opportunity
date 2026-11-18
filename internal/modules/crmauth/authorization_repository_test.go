package crmauth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type authorizationSQL struct {
	query              string
	writes             map[string]driver.Value
	commits, rollbacks int
}
type authorizationConnector struct{ state *authorizationSQL }
type authorizationDriver struct{ state *authorizationSQL }
type authorizationConnection struct{ state *authorizationSQL }
type authorizationTransaction struct{ state *authorizationSQL }
type authorizationRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

// Connect 建立只存在内存中的SQL协议连接。
// 参数：上下文为数据库连接请求。
// 返回值：记录真实GORM读写语句的测试连接，不访问网络。
func (c authorizationConnector) Connect(context.Context) (driver.Conn, error) {
	return &authorizationConnection{state: c.state}, nil
}

// Driver 返回内存连接的驱动。
// 参数：无。
// 返回值：无网络能力的SQL测试驱动。
func (c authorizationConnector) Driver() driver.Driver { return authorizationDriver{state: c.state} }

// Open 建立内存SQL测试连接。
// 参数：连接字符串不使用。
// 返回值：内存连接，无错误。
func (d authorizationDriver) Open(string) (driver.Conn, error) {
	return &authorizationConnection{state: d.state}, nil
}

// Prepare 禁止使用不需要的预处理语句。
// 参数：SQL文本不使用。
// 返回值：标准跳过标识。
func (*authorizationConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }

// Close 结束内存连接。
// 参数：无。
// 返回值：无错误。
func (*authorizationConnection) Close() error { return nil }

// Begin 建立记录提交与回滚的测试事务。
// 参数：无。
// 返回值：内存事务，无错误。
func (c *authorizationConnection) Begin() (driver.Tx, error) {
	return &authorizationTransaction{state: c.state}, nil
}

// Commit 记录真实仓储提交事务的动作。
// 参数：无。
// 返回值：无错误。
func (t *authorizationTransaction) Commit() error { t.state.commits++; return nil }

// Rollback 记录真实仓储回滚事务的动作。
// 参数：无。
// 返回值：无错误。
func (t *authorizationTransaction) Rollback() error { t.state.rollbacks++; return nil }

// QueryContext 按真实SELECT投影返回固定有效会话。
// 参数：上下文、查询语句和绑定参数来自GORM。
// 返回值：对应投影的内存行，不伪造不存在的完整会话字段。
func (c *authorizationConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.query = query
	if strings.Contains(query, "SELECT `session_id_hash`") {
		return &authorizationRows{columns: []string{"session_id_hash"}, values: []driver.Value{"session-hash"}}, nil
	}
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	return &authorizationRows{
		columns: []string{"session_id_hash", "tenant_id", "platform_user_id", "roles_json", "permissions_json", "data_scopes_json", "organization_ids_json", "authz_revision", "authorization_checked_at", "expires_at", "created_at", "last_interactive_at"},
		values:  []driver.Value{"session-hash", "tenant-a", "subject-a", `["sales"]`, `["customer.read","customer.create"]`, `[{"role_code":"sales","scope_type":"APPLICATION"}]`, `[]`, int64(3), now, now.Add(time.Hour), now, now},
	}, nil
}

// ExecContext 记录真实UPDATE写入的列和值。
// 参数：上下文、查询与绑定值来自生产仓储。
// 返回值：一条匹配记录的更新结果。
func (c *authorizationConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	start, end := strings.Index(query, " SET "), strings.Index(query, " WHERE ")
	if start < 0 || end < 0 {
		return nil, errors.New("unexpected statement")
	}
	c.state.writes = map[string]driver.Value{}
	for i, field := range strings.Split(query[start+5:end], ",") {
		c.state.writes[strings.Trim(strings.SplitN(field, "=", 2)[0], " `")] = args[i].Value
	}
	return driver.RowsAffected(1), nil
}

// Columns 返回本次查询真实投影列。
// 参数：无。
// 返回值：列名列表。
func (r *authorizationRows) Columns() []string { return r.columns }

// Close 结束内存结果集。
// 参数：无。
// 返回值：无错误。
func (*authorizationRows) Close() error { return nil }

// Next 逐次读取一条受控会话行。
// 参数：values为database/sql提供的当前行缓冲。
// 返回值：首行成功，其后返回EOF。
func (r *authorizationRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(values, r.values)
	return nil
}

// TestAuthorizationRepositoryLoadsAndPersistsCompleteSnapshot 验证生产仓储锁内读取完整会话且只保存新授权。
// 参数：t为GORM与无网络SQL连接的契约测试上下文。
// 返回值：无；只读到hash、遗漏授权字段或覆盖凭据期限时失败。
func TestAuthorizationRepositoryLoadsAndPersistsCompleteSnapshot(t *testing.T) {
	state := &authorizationSQL{}
	raw := sql.OpenDB(authorizationConnector{state: state})
	t.Cleanup(func() { _ = raw.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewGORMRepository(db)
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	session, err := repo.WithAuthorizationLock(context.Background(), "session-hash", now, time.Minute, func(ctx context.Context, current *Session) error {
		if current.PlatformUserID != "subject-a" || current.TenantID != "tenant-a" || len(current.PermissionsJSON) == 0 {
			t.Fatalf("locked read lost the session snapshot: %#v", current)
		}
		current.PermissionsJSON, current.AuthzRevision = []byte(`["customer.read"]`), 4
		current.AuthorizationCheckedAt = now.Add(time.Second)
		return nil
	})
	if err != nil || session.AuthzRevision != 4 {
		t.Fatalf("snapshot=%#v error=%v", session, err)
	}
	if !strings.Contains(state.query, "FOR UPDATE") {
		t.Fatalf("missing session boundary: %s", state.query)
	}
	if state.commits != 1 || state.writes["permissions_json"] != `["customer.read"]` || state.writes["authz_revision"] != int64(4) {
		t.Fatalf("snapshot not atomically persisted: %#v", state)
	}
	for _, forbidden := range []string{"expires_at", "absolute_expiry", "last_seen_at", "access_token_cipher", "refresh_token_cipher", "central_binding_cipher"} {
		if _, exists := state.writes[forbidden]; exists {
			t.Fatalf("authorization update overwrote %s", forbidden)
		}
	}
}

// TestAuthorizationRepositorySeparatesDenialAndDependency 验证403提交空权限而503回滚且不撤会话。
// 参数：t为生产仓储的内存SQL事务测试上下文。
// 返回值：无；错误分类或提交边界与授权安全语义不符时失败。
func TestAuthorizationRepositorySeparatesDenialAndDependency(t *testing.T) {
	for _, failure := range []error{sharedauthorization.ErrForbidden, sharedauthorization.ErrUnavailable} {
		state := &authorizationSQL{}
		raw := sql.OpenDB(authorizationConnector{state: state})
		t.Cleanup(func() { _ = raw.Close() })
		db, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
		if err != nil {
			t.Fatal(err)
		}
		repo := NewGORMRepository(db)
		_, err = repo.WithAuthorizationLock(context.Background(), "session-hash", time.Now().UTC(), time.Minute,
			func(_ context.Context, current *Session) error { clearSessionAuthorization(current); return failure })
		if !errors.Is(err, failure) {
			t.Fatalf("failure=%v returned=%v", failure, err)
		}
		if errors.Is(failure, sharedauthorization.ErrForbidden) {
			if state.commits != 1 || state.writes["permissions_json"] != "[]" {
				t.Fatalf("denial did not commit empty projection: %#v", state)
			}
			if _, exists := state.writes["revoked_at"]; exists {
				t.Fatal("403 revoked login")
			}
		} else if state.commits != 0 || len(state.writes) != 0 || state.rollbacks != 1 {
			t.Fatalf("dependency failure committed: %#v", state)
		}
	}
}
