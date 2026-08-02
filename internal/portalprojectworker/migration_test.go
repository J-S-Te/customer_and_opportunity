package portalprojectworker

import (
	"os"
	"strings"
	"testing"
)

func TestProjectSyncMigrationAvoidsMySQLCursorKeyword(t *testing.T) {
	sql, err := os.ReadFile("../../migrations/000016_portal_project_sync.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	statement := strings.ToLower(string(sql))
	if strings.Contains(statement, "\n  cursor varchar") || strings.Contains(statement, "customer_id, cursor,") {
		t.Fatal("project sync migration uses the MySQL CURSOR keyword as an unquoted column")
	}
	if !strings.Contains(statement, "sync_cursor varchar(1024)") || !strings.Contains(statement, "customer_id, sync_cursor,") {
		t.Fatal("project sync migration and seed must use sync_cursor")
	}
}
