package notification

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 连接器记录三个服务方法的真实 SQL 边界。
// 它仅提供确定性数据行，不模拟 MySQL 谓词、
// 锁或事务；这些仍须通过隔离数据库集成验证。
type inboxSQL struct{ queries []inboxStatement }
type inboxStatement struct {
	sql  string
	args []driver.NamedValue
}
type inboxConn struct{ script *inboxSQL }
type inboxRows struct {
	columns []string
	values  [][]driver.Value
}

func (s *inboxSQL) Connect(context.Context) (driver.Conn, error) { return &inboxConn{script: s}, nil }
func (s *inboxSQL) Driver() driver.Driver                        { return inboxDriver{} }

type inboxDriver struct{}

func (inboxDriver) Open(string) (driver.Conn, error)   { return nil, errors.New("use connector") }
func (*inboxConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (*inboxConn) Close() error                        { return nil }
func (*inboxConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected transaction") }
func (c *inboxConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.script.queries = append(c.script.queries, inboxStatement{query, args})
	if strings.Contains(query, "count(*)") {
		return &inboxRows{[]string{"count"}, [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.HasPrefix(query, "SELECT `status`") {
		for _, arg := range args {
			if arg.Value == int64(3) {
				return &inboxRows{columns: []string{"status"}}, nil
			}
		}
		return &inboxRows{[]string{"status"}, [][]driver.Value{{"READ"}}}, nil
	}
	return &inboxRows{[]string{"id", "type", "status"}, [][]driver.Value{{int64(1), TypePresaleAssigneeAdded, "UNREAD"}}}, nil
}
func (c *inboxConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.script.queries = append(c.script.queries, inboxStatement{query, args})
	for _, arg := range args {
		if arg.Value == int64(1) {
			return driver.RowsAffected(1), nil
		}
	}
	return driver.RowsAffected(0), nil
}
func (r *inboxRows) Columns() []string { return r.columns }
func (*inboxRows) Close() error        { return nil }
func (r *inboxRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func TestInboxListUnreadAndReadKeepPlatformUserBoundary(t *testing.T) {
	for _, personID := range []string{"", "foreign-pms-person"} {
		t.Run("person="+personID, func(t *testing.T) {
			script := &inboxSQL{}
			pool := sql.OpenDB(script)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(db)
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "user-a", PersonID: personID, Permissions: map[string]struct{}{"presale.read": {}}})
			page, err := service.ListMine(ctx, false, 1, 20)
			if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Type != TypePresaleAssigneeAdded {
				t.Fatalf("page=%+v error=%v", page, err)
			}
			if count, err := service.UnreadCount(ctx); err != nil || count != 1 {
				t.Fatalf("count=%d error=%v", count, err)
			}
			if err := service.MarkRead(ctx, 1); err != nil {
				t.Fatalf("mark read: %v", err)
			}
			if err := service.MarkRead(ctx, 2); err != nil {
				t.Fatalf("already read: %v", err)
			}
			if err := service.MarkRead(ctx, 3); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing/foreign message: %v", err)
			}
			if len(script.queries) != 8 {
				t.Fatalf("service SQL operations=%d", len(script.queries))
			}
			for _, statement := range script.queries {
				where := statement.sql[strings.Index(statement.sql, "WHERE"):]
				if !strings.Contains(where, "tenant_id=?") || !strings.Contains(where, "recipient_namespace=? AND recipient_id=?") {
					t.Fatalf("missing identity boundary: %s", statement.sql)
				}
				var user, namespace, tenant bool
				for _, arg := range statement.args {
					if personID != "" && arg.Value == personID {
						t.Fatalf("external person leaked into inbox predicate: %v", statement.args)
					}
					user = user || arg.Value == "user-a"
					namespace = namespace || arg.Value == "USER"
					tenant = tenant || arg.Value == "tenant-a"
				}
				if !user || !namespace || !tenant {
					t.Fatalf("missing bound identity: %v", statement.args)
				}
			}
		})
	}
}
