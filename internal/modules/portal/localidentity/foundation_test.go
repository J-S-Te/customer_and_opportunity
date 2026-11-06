package localidentity

import (
	"context"
	"errors"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm/logger"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"strings"
)

// foundationDB 在明确空库中执行Portal真实迁移及本地迁移重放。
// 参数：t 为测试上下文。
// 返回值：本地身份仓储，不连接业务库。
func foundationDB(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("PORTAL_LOCAL_TEST_DSN")
	if dsn == "" {
		t.Skip("requires dedicated empty PORTAL_LOCAL_TEST_DSN; no connection attempted")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "portal_local_test") {
		t.Fatal("requires portal_local_test schema")
	}
	host, portRaw, err := net.SplitHostPort(cfg.Addr)
	port, portErr := strconv.Atoi(portRaw)
	if err != nil || portErr != nil || host != "127.0.0.1" || cfg.Net != "tcp" || port < 32768 {
		t.Fatal("requires explicit disposable loopback high-port MySQL")
	}
	cfg.MultiStatements = true
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated database failed")
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = pool.Close() })
	var tables int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&tables).Error; err != nil || tables != 0 {
		t.Fatal("requires empty isolated schema")
	}
	t.Cleanup(func() {
		cleanup, err := pool.Conn(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		defer cleanup.Close()
		// 仅本轮已证实为空的隔离库使用同一连接清理外键依赖，恢复失败即测试失败。
		if _, err := cleanup.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS=0"); err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if _, err := cleanup.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS=1"); err != nil {
				t.Error(err)
			}
		}()
		rows, err := cleanup.QueryContext(context.Background(), "SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE()")
		if err != nil {
			t.Error(err)
			return
		}
		names := []string{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Error(err)
				rows.Close()
				return
			}
			if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(name) {
				t.Error("unexpected table name")
				rows.Close()
				return
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Error(err)
		}
		rows.Close()
		for _, name := range names {
			if _, err := cleanup.ExecContext(context.Background(), "DROP TABLE `"+name+"`"); err != nil {
				t.Error(err)
			}
		}
	})

	files, err := migrationplan.Files(migrationplan.Portal)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range files {
		if name == "000129_portal_local_identity.up.sql" {
			found = true
		}
	}
	if !found {
		files = append(files, "000129_portal_local_identity.up.sql")
	}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join("../../../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(string(raw)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	// 新迁移独立幂等重放；旧迁移不在既有数据库上重复执行。
	raw, err := os.ReadFile("../../../../migrations/000129_portal_local_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	return NewRepository(db)
}

// TestModeAndNamespaceFailClosed 验证兼容默认与本地来源显式命名空间。
// 参数：t 为测试上下文。
// 返回值：无；跨来源数字被接受时失败。
func TestModeAndNamespaceFailClosed(t *testing.T) {
	if mode, err := ParseMode(""); err != nil || mode != ModeCRM {
		t.Fatal(mode, err)
	}
	if _, err := ParseMode("mixed"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, namespace := range []string{"", "crm", "LOCAL"} {
		if GuardCustomerInput(ModeLocal, namespace, 42) == nil {
			t.Fatalf("legacy namespace %q accepted", namespace)
		}
	}
	if err := GuardCustomerInput(ModeLocal, LocalNamespace, 42); err != nil {
		t.Fatal(err)
	}
	if err := GuardCustomerInput(ModeCRM, "", 42); err != nil {
		t.Fatal(err)
	}
}

// TestAuthorityRejectsLegacyAndSourceSwitch 验证单租户权威锁与旧数据切换防线。
// 参数：t 为测试上下文。
// 返回值：无；旧客户或来源切换被放行时失败。
func TestAuthorityRejectsLegacyAndSourceSwitch(t *testing.T) {
	repo := foundationDB(t)
	ctx := context.Background()
	if err := repo.CheckMode(ctx, "tenant", ModeLocal); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimMode(ctx, "tenant", ModeLocal, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckMode(ctx, "tenant", ModeCRM); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatalf("mode switch=%v", err)
	}
	if err := repo.db.Create(&account.IdentityLink{Model: database.Model{TenantID: "legacy", CreatedBy: "fixture", UpdatedBy: "fixture", Version: 1}, CustomerID: 42, PlatformUserID: "legacy-subject", AccountNo: "legacy-account", Status: account.IdentityPending}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckMode(ctx, "legacy", ModeLocal); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatalf("legacy business=%v", err)
	}
	if err := repo.CheckMode(ctx, "legacy", ModeCRM); err != nil {
		t.Fatal(err)
	}
}
