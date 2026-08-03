package migrationplan

import (
	"os"
	"strings"
	"testing"
)

func TestSplitStatementsPreservesQuotedAndCommentSemicolons(t *testing.T) {
	input := "-- ignored ;\nCREATE TABLE `a;b` (v VARCHAR(20) DEFAULT 'x;y'); # ignored ;\n" +
		"INSERT INTO `a;b` VALUES (\"z;w\"); /* ignored ; */"
	statements, err := SplitStatements(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 || !strings.Contains(statements[0], "CREATE TABLE") || !strings.Contains(statements[1], "INSERT INTO") {
		t.Fatalf("statements = %#v", statements)
	}
}

func TestSplitStatementsRejectsAmbiguousInput(t *testing.T) {
	for _, input := range []string{"-- comment only", "SELECT 'unterminated", "SELECT 1; /* open"} {
		if _, err := SplitStatements(input); err == nil {
			t.Fatalf("expected error for %q", input)
		}
	}
}

func TestRepositoryMigrationsSplitIntoStatements(t *testing.T) {
	for _, schema := range Schemas() {
		plan, err := Build("../../migrations", schema)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range plan.Entries {
			contents, readErr := os.ReadFile("../../migrations/" + entry.File)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, splitErr := SplitStatements(string(contents)); splitErr != nil {
				t.Fatalf("%s: %v", entry.File, splitErr)
			}
		}
	}
}
