// Command production-migrate applies the immutable CRM or Portal migration plan
// with a durable checkpoint for every SQL statement. It is intended only for the
// controlled production release container and refuses unmanaged existing schemas.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
)

const metadataTable = "app_schema_migration_statements"

func main() {
	schemaFlag := flag.String("schema", "", "target schema: crm or portal")
	directory := flag.String("dir", "migrations", "immutable migration directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("production-migrate accepts flags only")
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

	plan, err := migrationplan.Build(*directory, schema)
	if err != nil {
		fatalf("build %s migration plan: %v", schema, err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fatalf("open %s database: %v", schema, err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		fatalf("connect to %s database: %v", schema, err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalf("reserve %s migration connection: %v", schema, err)
	}
	defer conn.Close()
	lockName := "customer-opportunity:production-migrate:" + string(schema)
	var locked int
	if err = conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 30)`, lockName).Scan(&locked); err != nil || locked != 1 {
		fatalf("acquire %s migration lock: %v", schema, err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK(?)`, lockName) }()

	managed, err := metadataExists(ctx, conn)
	if err != nil {
		fatalf("inspect %s migration metadata: %v", schema, err)
	}
	if !managed {
		count, countErr := applicationTableCount(ctx, conn)
		if countErr != nil {
			fatalf("inspect existing %s schema: %v", schema, countErr)
		}
		if count != 0 {
			fatalf("refusing unmanaged existing %s schema with %d tables; establish a reviewed production baseline before retrying", schema, count)
		}
	}
	if err = createMetadata(ctx, conn); err != nil {
		fatalf("create %s migration metadata: %v", schema, err)
	}
	if file, position, ok, runningErr := ambiguousCheckpoint(ctx, conn, schema); runningErr != nil {
		fatalf("read %s migration checkpoints: %v", schema, runningErr)
	} else if ok {
		fatalf("ambiguous production migration checkpoint: schema=%s file=%s statement=%d; verify the database structure and resolve the checkpoint explicitly before retrying", schema, file, position)
	}

	for _, entry := range plan.Entries {
		contents, readErr := os.ReadFile(filepath.Join(*directory, entry.File))
		if readErr != nil {
			fatalf("read migration %s: %v", entry.File, readErr)
		}
		statements, splitErr := migrationplan.SplitStatements(string(contents))
		if splitErr != nil {
			fatalf("split migration %s: %v", entry.File, splitErr)
		}
		for index, statement := range statements {
			position := index + 1
			statementChecksum := checksum(statement)
			applied, checkpointErr := checkpointApplied(ctx, conn, schema, entry.File, entry.SHA256, position, statementChecksum)
			if checkpointErr != nil {
				fatalf("validate migration checkpoint %s statement %d: %v", entry.File, position, checkpointErr)
			}
			if applied {
				fmt.Printf("skip %s statement %d/%d\n", entry.File, position, len(statements))
				continue
			}
			if _, err = conn.ExecContext(ctx, `INSERT INTO `+metadataTable+`
(schema_name,migration_file,file_checksum,statement_position,statement_checksum,status,started_at)
VALUES (?,?,?,?,?,'RUNNING',UTC_TIMESTAMP(3))`, string(schema), entry.File, entry.SHA256, position, statementChecksum); err != nil {
				fatalf("start migration %s statement %d: %v", entry.File, position, err)
			}
			fmt.Printf("apply %s statement %d/%d\n", entry.File, position, len(statements))
			if _, err = conn.ExecContext(ctx, statement); err != nil {
				fatalf("apply migration %s statement %d: %v; checkpoint remains RUNNING for explicit review", entry.File, position, err)
			}
			result, updateErr := conn.ExecContext(ctx, `UPDATE `+metadataTable+`
SET status='APPLIED', applied_at=UTC_TIMESTAMP(3)
WHERE schema_name=? AND migration_file=? AND statement_position=? AND status='RUNNING'`, string(schema), entry.File, position)
			if updateErr != nil {
				fatalf("finish migration %s statement %d: %v", entry.File, position, updateErr)
			}
			if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
				fatalf("finish migration %s statement %d: checkpoint fencing failed", entry.File, position)
			}
		}
	}
	fmt.Printf("%s production migrations are current; combined_checksum=%s\n", schema, migrationplan.CombinedChecksum(plan))
}

func metadataExists(ctx context.Context, db *sql.Conn) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
WHERE table_schema=DATABASE() AND table_name=?`, metadataTable).Scan(&count)
	return count == 1, err
}

func applicationTableCount(ctx context.Context, db *sql.Conn) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
WHERE table_schema=DATABASE() AND table_type='BASE TABLE'`).Scan(&count)
	return count, err
}

func createMetadata(ctx context.Context, db *sql.Conn) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+metadataTable+` (
schema_name VARCHAR(16) NOT NULL,
migration_file VARCHAR(160) NOT NULL,
file_checksum CHAR(71) NOT NULL,
statement_position INT UNSIGNED NOT NULL,
statement_checksum CHAR(71) NOT NULL,
status VARCHAR(16) NOT NULL,
started_at DATETIME(3) NOT NULL,
applied_at DATETIME(3) NULL,
PRIMARY KEY (schema_name,migration_file,statement_position),
KEY idx_schema_migration_status (schema_name,status,started_at),
CONSTRAINT chk_schema_migration_status CHECK (status IN ('RUNNING','APPLIED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`)
	return err
}

func ambiguousCheckpoint(ctx context.Context, db *sql.Conn, schema migrationplan.Schema) (string, int, bool, error) {
	var file string
	var position int
	err := db.QueryRowContext(ctx, `SELECT migration_file,statement_position FROM `+metadataTable+`
WHERE schema_name=? AND status='RUNNING' ORDER BY started_at,migration_file,statement_position LIMIT 1`, string(schema)).Scan(&file, &position)
	if err == sql.ErrNoRows {
		return "", 0, false, nil
	}
	return file, position, err == nil, err
}

func checkpointApplied(ctx context.Context, db *sql.Conn, schema migrationplan.Schema, file, fileChecksum string, position int, statementChecksum string) (bool, error) {
	var storedFileChecksum, storedStatementChecksum, status string
	err := db.QueryRowContext(ctx, `SELECT file_checksum,statement_checksum,status FROM `+metadataTable+`
WHERE schema_name=? AND migration_file=? AND statement_position=?`, string(schema), file, position).Scan(&storedFileChecksum, &storedStatementChecksum, &status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedFileChecksum != fileChecksum || storedStatementChecksum != statementChecksum {
		return false, fmt.Errorf("immutable checksum changed")
	}
	if status != "APPLIED" {
		return false, fmt.Errorf("checkpoint status is %s", status)
	}
	return true, nil
}

func checksum(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
