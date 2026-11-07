package migrationplan

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRepositoryMigrationsAreAssignedExactlyOnce 验证CRM与门户迁移互斥且均被登记。
// 参数：t为测试上下文。
// 返回值：无；新增版本未归属或迁移重复时失败。
func TestRepositoryMigrationsAreAssignedExactlyOnce(t *testing.T) {
	directory := filepath.Join("..", "..", "migrations")
	crm, err := Build(directory, CRM)
	if err != nil {
		t.Fatal(err)
	}
	portal, err := Build(directory, Portal)
	if err != nil {
		t.Fatal(err)
	}
	if len(crm.Entries) != 81 || len(portal.Entries) != 44 {
		t.Fatalf("unexpected plan lengths: crm=%d portal=%d", len(crm.Entries), len(portal.Entries))
	}
	for _, plan := range []Plan{crm, portal} {
		checksum := CombinedChecksum(plan)
		if !strings.HasPrefix(checksum, "sha256:") || len(checksum) != 71 {
			t.Fatalf("invalid combined checksum: %q", checksum)
		}
	}
}

func TestBuildRejectsUnassignedAndEmptyMigrations(t *testing.T) {
	directory := t.TempDir()
	files, err := Files(CRM)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(DeclaredFiles(), "999999_unassigned.up.sql") {
		if err = os.WriteFile(filepath.Join(directory, name), []byte("SELECT 1;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = Build(directory, CRM); err == nil || !strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("unassigned migration error = %v", err)
	}
	if err = os.Remove(filepath.Join(directory, "999999_unassigned.up.sql")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, files[0]), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Build(directory, CRM); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty migration error = %v", err)
	}
}

// TestIntegratedSessionMigrationsPreservePublishedDDL 验证已发布中央迁移不变且增量不重复建列。
// 参数：t为迁移兼容性测试上下文。
// 返回值：无；历史摘要漂移、重叠列或顺序错误时失败。
func TestIntegratedSessionMigrationsPreservePublishedDDL(t *testing.T) {
	directory := filepath.Join("..", "..", "migrations")
	published := map[string]string{
		"000124_central_session_governance.up.sql":        "31d82b8d9ad816b1a1758cf7ae094beb997a18250878f34cd9708b7dbf7a49c2",
		"000125_portal_central_session_governance.up.sql": "f21f51e82627481fd792ded7229cf129f196fd60749f386717b4d9324e897733",
	}
	for name, expected := range published {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(body)); actual != expected {
			t.Fatalf("published migration changed: %s", name)
		}
	}
	plan, err := Build(directory, CRM)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]int{}
	for _, entry := range plan.Entries {
		body, err := os.ReadFile(filepath.Join(directory, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range regexp.MustCompile(`(?i)ADD\s+COLUMN\s+(\w+)`).FindAllStringSubmatch(string(body), -1) {
			columns[strings.ToLower(match[1])]++
		}
	}
	for _, column := range []string{"refresh_token_cipher", "access_token_expires_at", "last_interactive_at"} {
		if columns[column] != 1 {
			t.Fatalf("column %s is added %d times", column, columns[column])
		}
	}
	last := plan.Entries[len(plan.Entries)-3:]
	if last[0].File != "000127_crm_handover_gate.up.sql" ||
		last[1].File != "000128_crm_opportunity_win_confirmations.up.sql" ||
		last[2].File != "000130_crm_request_audit_outbox_delivered_index.up.sql" {
		t.Fatalf("new migrations must follow published main: %v", last)
	}
}
