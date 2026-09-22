package opportunity

import (
	"os"
	"strings"
	"testing"
)

func TestOpportunityCatalogDefaultsAndKinds(t *testing.T) {
	if len(defaultCatalogItems[CatalogKindType]) != 18 {
		t.Fatalf("type defaults=%d want=18", len(defaultCatalogItems[CatalogKindType]))
	}
	if len(defaultCatalogItems[CatalogKindSource]) != 10 {
		t.Fatalf("source defaults=%d want=10", len(defaultCatalogItems[CatalogKindSource]))
	}
	if !validCatalogKind(CatalogKindType) || !validCatalogKind(CatalogKindSource) || validCatalogKind("OTHER") {
		t.Fatal("catalog kind validation is inconsistent")
	}
}

func TestNormalizeOpportunityCatalogName(t *testing.T) {
	name, normalized, err := normalizeCatalogName("  客户 推荐  ")
	if err != nil || name != "客户 推荐" || normalized != "客户推荐" {
		t.Fatalf("unexpected normalized catalog name: %q %q %v", name, normalized, err)
	}
	for _, value := range []string{"", "   ", "类型、来源", "名称\n注入", strings.Repeat("类", 65)} {
		if _, _, err = normalizeCatalogName(value); err == nil {
			t.Fatalf("invalid catalog name accepted: %q", value)
		}
	}
}

func TestCatalogIDSelectionPreservesOrderAndRemovesDuplicates(t *testing.T) {
	got := uniqueCatalogIDs([]uint64{3, 2, 3, 0, 1, 2})
	want := []uint64{3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

func TestOpportunityCatalogMigrationProtectsReferencesAndBackfillsLegacyValues(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000110_opportunity_catalog.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, required := range []string{
		"CREATE TABLE crm_opportunity_catalog_items",
		"CREATE TABLE crm_opportunity_catalog_links",
		"FOREIGN KEY (tenant_id, catalog_item_id, kind)",
		"ON UPDATE RESTRICT ON DELETE RESTRICT",
		"WITH RECURSIVE catalog_split",
		"source_split AS",
		"INSERT IGNORE INTO crm_opportunity_catalog_links",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(content), "ON DELETE CASCADE") {
		t.Fatal("catalog migration must not cascade deletes")
	}
	initializations, err := os.ReadFile("../../../migrations/000111_opportunity_catalog_initializations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	initializationContent := string(initializations)
	for _, required := range []string{
		"CREATE TABLE crm_opportunity_catalog_initializations",
		"SELECT tenant_id FROM crm_opportunity_catalog_items",
		"INSERT IGNORE INTO crm_opportunity_catalog_initializations",
	} {
		if !strings.Contains(initializationContent, required) {
			t.Fatalf("initialization migration missing %q", required)
		}
	}
}
