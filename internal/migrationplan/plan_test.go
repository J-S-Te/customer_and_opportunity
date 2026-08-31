package migrationplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if len(crm.Entries) != 65 || len(portal.Entries) != 36 {
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
