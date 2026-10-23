package presale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTwoLevelAssignmentMigrationExtendsMutationReplayChecks 防止迁移只加列而漏掉
// 幂等账本的 CHECK。真实 MySQL 已出现 level-1 写入因旧约束而 500 的回归，这里把
// 迁移文件中的必需 operation/action 作为源码级门禁。
func TestTwoLevelAssignmentMigrationExtendsMutationReplayChecks(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "000114_presale_two_level_assignment.up.sql")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, required := range []string{
		"DROP CHECK chk_presale_mutation_operation_action",
		"DROP CHECK chk_presale_mutation_action",
		"DROP CHECK chk_presale_mutation_operation",
		"'SELECT_EXECUTION_DEPARTMENT'",
		"'REPLACE_EXECUTOR_ASSIGNMENTS'",
		"'REJECT_EXECUTION_ASSIGNMENT'",
		"'TEAM_LEAD'",
		"'EXECUTOR'",
		"'REJECT'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("000114 up migration missing %q; mutation replay CHECK would reject TS-011 writes", required)
		}
	}
}

func TestMultiApprovalMigrationCoversThirdAndLaterNodes(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "000117_presale_multi_approval_nodes.up.sql")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"NODE_3_PASS", "NODE_3_REJECT", "NODE_10_PASS", "NODE_10_REJECT"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("000117 migration missing %s", required)
		}
	}
}
