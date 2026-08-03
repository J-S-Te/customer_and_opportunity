// Command local-migrate 只向本地开发数据库应用仓库维护的 CRM 或 Portal 迁移计划；生产发布仍须
// 使用发布平台及 migrations/README.md 规定的语句级控制。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
)

const metadataTable = "app_local_schema_migrations"

func main() {
	schemaFlag := flag.String("schema", "crm", "target schema: crm or portal")
	directory := flag.String("dir", "migrations", "migration directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("local-migrate accepts flags only")
	}

	schema := migrationplan.Schema(strings.ToLower(strings.TrimSpace(*schemaFlag)))
	if schema != migrationplan.CRM && schema != migrationplan.Portal {
		fatalf("-schema must be crm or portal")
	}
	dsnKey := "MYSQL_DSN"
	if schema == migrationplan.Portal {
		dsnKey = "PORTAL_MYSQL_DSN"
	}
	dsn := strings.TrimSpace(os.Getenv(dsnKey))
	if dsn == "" {
		fatalf("%s is required", dsnKey)
	}
	if !strings.Contains(dsn, "multiStatements=true") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		dsn += separator + "multiStatements=true"
	}

	plan, err := migrationplan.Build(*directory, schema)
	if err != nil {
		fatalf("build %s migration plan: %v", schema, err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fatalf("open %s database: %v", schema, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		fatalf("connect to %s database: %v", schema, err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+metadataTable+` (
schema_name VARCHAR(16) NOT NULL,
migration_file VARCHAR(160) NOT NULL,
checksum VARCHAR(80) NOT NULL,
applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
PRIMARY KEY (schema_name, migration_file)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		fatalf("create local migration metadata: %v", err)
	}

	for _, entry := range plan.Entries {
		var current string
		err = db.QueryRowContext(ctx,
			`SELECT checksum FROM `+metadataTable+` WHERE schema_name = ? AND migration_file = ?`,
			string(schema), entry.File,
		).Scan(&current)
		if err == nil {
			if current != entry.SHA256 {
				fatalf("migration checksum changed after application: %s", entry.File)
			}
			fmt.Printf("skip %s (%d/%d)\n", entry.File, entry.Position, len(plan.Entries))
			continue
		}
		if err != sql.ErrNoRows {
			fatalf("read migration metadata for %s: %v", entry.File, err)
		}

		contents, readErr := os.ReadFile(filepath.Join(*directory, entry.File))
		if readErr != nil {
			fatalf("read migration %s: %v", entry.File, readErr)
		}
		fmt.Printf("apply %s (%d/%d)\n", entry.File, entry.Position, len(plan.Entries))
		// MySQL DDL 会自动提交，元数据行无法让整个迁移文件原子化。本地执行器仅在全部成功后记录，
		// 部分 DDL 成功后的重跑会失败关闭；生产使用更严格的语句级发布流程。
		if _, err = db.ExecContext(ctx, string(contents)); err != nil {
			fatalf("apply migration %s: %v", entry.File, err)
		}
		if _, err = db.ExecContext(ctx,
			`INSERT INTO `+metadataTable+` (schema_name, migration_file, checksum) VALUES (?, ?, ?)`,
			string(schema), entry.File, entry.SHA256,
		); err != nil {
			fatalf("record migration %s: %v", entry.File, err)
		}
	}
	fmt.Printf("%s local migrations are current; combined_checksum=%s\n", schema, migrationplan.CombinedChecksum(plan))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
