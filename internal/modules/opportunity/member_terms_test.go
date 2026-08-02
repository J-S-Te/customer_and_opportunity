package opportunity

import (
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestMemberTermQueryBindsTenantParentAndOptionalFilters(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	query := db.Model(&MemberTerm{}).
		Where("tenant_id=? AND opportunity_id=?", "tenant-a", 7).
		Where("user_id=?", "sub-a").Where("active_user_id IS NOT NULL")
	var terms []MemberTerm
	statement := query.Order("COALESCE(started_at,snapshot_at) DESC").Order("id DESC").Limit(20).Find(&terms).Statement.SQL.String()
	for _, fragment := range []string{"tenant_id", "opportunity_id", "user_id", "active_user_id IS NOT NULL", "COALESCE(started_at,snapshot_at) DESC", "id DESC"} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("missing %q in %q", fragment, statement)
		}
	}
}

func TestMemberTermMigrationIsTenantScopedAndDoesNotFabricateOldTerms(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000053_opportunity_member_terms.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"FOREIGN KEY (tenant_id,opportunity_id)", "FOREIGN KEY (tenant_id,member_id)",
		"uq_opportunity_member_active_term", "snapshot_at", "active_at_snapshot", "LEGACY_SNAPSHOT", "RECORDED",
		"SELECT tenant_id,opportunity_id,id,user_id,role,NULL,CURRENT_TIMESTAMP(3),is_active",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(source, "crm_audit_events") {
		t.Fatal("migration must not manufacture membership intervals from business audit JSON")
	}
	if strings.Contains(source, "user_id,role,created_at") || strings.Contains(source, "COALESCE(ended_at,updated_at)") {
		t.Fatal("migration must not treat a reusable member row as a continuous historical term")
	}
}

func TestLegacyMemberTermDTOExposesSnapshotInsteadOfInventedBoundaries(t *testing.T) {
	snapshot := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	active := true
	value := memberTermResponse(MemberTerm{ID: 1, UserID: "sub-a", Role: MemberRoleOther, SnapshotAt: &snapshot, ActiveAtSnapshot: &active, SourceKind: MemberTermSourceLegacySnapshot})
	if value.StartedAt != nil || value.EndedAt != nil || value.StartedBy != nil || value.SnapshotAt == nil || value.ActiveAtSnapshot == nil || !*value.ActiveAtSnapshot {
		t.Fatalf("legacy snapshot DTO fabricated a membership boundary: %#v", value)
	}
}
