package opportunity

import (
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func newCatalogProjectionDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	return db
}

func TestSplitCatalogProjectionBatches(t *testing.T) {
	if got := len(splitCatalogProjectionBatches(nil)); got != 0 {
		t.Fatalf("empty links produced %d batches, want 0", got)
	}
	if got := splitCatalogProjectionBatches([]uint64{1, 2}); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("small batch split = %#v", got)
	}
	// 超过 500 条关联时必须按 500 分批，批次大小依次为 500、500、201。
	ids := make([]uint64, 0, 1201)
	for index := 1; index <= 1201; index++ {
		ids = append(ids, uint64(index))
	}
	batches := splitCatalogProjectionBatches(ids)
	if len(batches) != 3 {
		t.Fatalf("batch count = %d, want 3", len(batches))
	}
	if len(batches[0]) != catalogProjectionBatchSize || len(batches[1]) != catalogProjectionBatchSize || len(batches[2]) != 201 {
		t.Fatalf("batch sizes = %d/%d/%d, want 500/500/201", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if batches[0][0] != 1 || batches[1][0] != 501 || batches[2][0] != 1001 || batches[2][200] != 1201 {
		t.Fatalf("batch boundaries wrong: %#v", batches)
	}
}

func TestCatalogProjectionColumn(t *testing.T) {
	if got := catalogProjectionColumn(CatalogKindType); got != "type" {
		t.Fatalf("type column = %q", got)
	}
	if got := catalogProjectionColumn(CatalogKindSource); got != "source" {
		t.Fatalf("source column = %q", got)
	}
}

// 名称查询必须保持历史逐行口径：JOIN 目录项表、不按 enabled 过滤（禁用目录项的名称
// 保留在投影中）、逐商机按 sort_order 与 item.id 排序。
func TestCatalogProjectionNameQueryKeepsDisabledItemsAndOrder(t *testing.T) {
	db := newCatalogProjectionDryRunDB(t)
	var names []catalogProjectionName
	statement := catalogProjectionNameQuery(db, "tenant-a", CatalogKindType, []uint64{9, 7}).Scan(&names).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{
		"crm_opportunity_catalog_links AS link",
		"JOIN crm_opportunity_catalog_items AS item ON item.tenant_id=link.tenant_id AND item.id=link.catalog_item_id",
		"link.tenant_id=?", "link.kind=?", "link.opportunity_id IN (?,?)",
		"link.opportunity_id ASC,link.sort_order ASC,item.id ASC",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing %q in %q", fragment, sql)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "ENABLED") {
		t.Fatalf("disabled catalog items must stay in projection: %q", sql)
	}
}

// 投影值必须与历史逐行行为一致：按「、」聚合全部关联目录项名称，每条 WHEN 绑定
// 一个商机及其聚合值；列名按 kind 选择且经过反引号包裹。
func TestCatalogProjectionUpdateQueryWritesJoinedLabels(t *testing.T) {
	db := newCatalogProjectionDryRunDB(t)
	ids := []uint64{9, 7}
	statement := catalogProjectionUpdateQuery(db, catalogProjectionColumn(CatalogKindType), "tenant-a", ids, map[uint64][]string{
		9: {"类型A", "类型B"},
		7: {"类型C"},
	}).Statement
	sql := statement.SQL.String()
	if !strings.HasPrefix(sql, "UPDATE crm_opportunities SET `type` = CASE `id`") {
		t.Fatalf("unexpected update prefix: %q", sql)
	}
	if strings.Count(sql, " WHEN ? THEN ?") != 2 {
		t.Fatalf("WHEN branches wrong: %q", sql)
	}
	if !strings.HasSuffix(sql, "END WHERE `tenant_id` = ? AND `id` IN (?,?)") {
		t.Fatalf("unexpected update suffix: %q", sql)
	}
	// WHEN 对每个商机绑定一对变量，IN 列表按原顺序展开为独立绑定变量。
	wantVars := []any{uint64(9), "类型A、类型B", uint64(7), "类型C", "tenant-a", uint64(9), uint64(7)}
	if len(statement.Vars) != len(wantVars) {
		t.Fatalf("vars = %#v", statement.Vars)
	}
	for index, want := range wantVars {
		if statement.Vars[index] != want {
			t.Fatalf("vars[%d] = %#v, want %#v", index, statement.Vars[index], want)
		}
	}

	source := catalogProjectionUpdateQuery(db, catalogProjectionColumn(CatalogKindSource), "tenant-a", ids, map[uint64][]string{9: {"来源A"}}).Statement
	if !strings.HasPrefix(source.SQL.String(), "UPDATE crm_opportunities SET `source` = CASE `id`") {
		t.Fatalf("source column not projected: %q", source.SQL.String())
	}
}

// 大批次的 WHEN/IN 绑定必须完整覆盖整批商机，不会因批大小被截断。
func TestCatalogProjectionUpdateQueryCoversFullBatch(t *testing.T) {
	db := newCatalogProjectionDryRunDB(t)
	batch := make([]uint64, 0, catalogProjectionBatchSize)
	labels := make(map[uint64][]string, catalogProjectionBatchSize)
	for index := 1; index <= catalogProjectionBatchSize; index++ {
		id := uint64(index)
		batch = append(batch, id)
		labels[id] = []string{fmt.Sprintf("类型%d", index)}
	}
	statement := catalogProjectionUpdateQuery(db, "type", "tenant-a", batch, labels).Statement
	if strings.Count(statement.SQL.String(), " WHEN ? THEN ?") != catalogProjectionBatchSize {
		t.Fatal("WHEN branch count does not cover the full batch")
	}
	// WHEN 对 500×2 个变量，tenant_id 1 个，IN 按 500 个商机展开。
	if len(statement.Vars) != catalogProjectionBatchSize*3+1 {
		t.Fatalf("vars count = %d, want %d", len(statement.Vars), catalogProjectionBatchSize*3+1)
	}
	if !strings.Contains(statement.SQL.String(), " IN ("+strings.TrimSuffix(strings.Repeat("?,", catalogProjectionBatchSize), ",")+")") {
		t.Fatal("IN placeholders do not cover the full batch")
	}
}
