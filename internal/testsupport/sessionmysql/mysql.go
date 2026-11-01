// Package sessionmysql 仅为会话集成测试建立显式隔离库，业务入口不引用本包。
package sessionmysql

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open 使用工程真实迁移计划创建本轮专用CRM或Portal数据库连接。
// 参数：t为测试上下文，directory为迁移目录，schema为业务计划，databaseName为固定测试库名。
// 返回值：真实GORM连接；缺专用变量时跳过，非回环高端口或计划执行失败时终止。
func Open(t *testing.T, directory string, schema migrationplan.Schema, databaseName string) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("SESSION_GOVERNANCE_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated SESSION_GOVERNANCE_TEST_DSN is not configured")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated session DSN")
	}
	host, portText, err := net.SplitHostPort(config.Addr)
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || host != "127.0.0.1" || config.Net != "tcp" || port < 32768 ||
		config.DBName != databaseName || (databaseName != "qa_session_crm_20261101" && databaseName != "qa_session_portal_20261101") {
		t.Fatal("refusing non-isolated session test database")
	}
	config.MultiStatements = true
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated session database failed")
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	plan, err := migrationplan.Build(directory, schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan.Entries {
		body, err := os.ReadFile(filepath.Join(directory, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply isolated %s migration %s: %v", schema, entry.File, err)
		}
	}
	return db
}
