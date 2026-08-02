package main

import (
	"os"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/migrationplan"
)

func TestRepositoryPlansRemainAvailableToLocalRunner(t *testing.T) {
	for _, schema := range migrationplan.Schemas() {
		plan, err := migrationplan.Build("../../migrations", schema)
		if err != nil {
			t.Fatalf("build %s plan: %v", schema, err)
		}
		if len(plan.Entries) == 0 || !strings.HasPrefix(migrationplan.CombinedChecksum(plan), "sha256:") {
			t.Fatalf("invalid %s plan: %+v", schema, plan)
		}
	}
}

func TestMigrationFilesDoNotContainClientDelimiterCommands(t *testing.T) {
	for _, schema := range migrationplan.Schemas() {
		plan, err := migrationplan.Build("../../migrations", schema)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range plan.Entries {
			contents, readErr := os.ReadFile("../../migrations/" + entry.File)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(strings.ToUpper(string(contents)), "DELIMITER ") {
				t.Fatalf("%s contains a mysql-client-only DELIMITER command", entry.File)
			}
		}
	}
}
