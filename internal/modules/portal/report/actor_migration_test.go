package report

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReportActorMigrationMatchesOIDCAccountIdentifierWidth(t *testing.T) {
	up, err := os.ReadFile("../../../../migrations/000041_portal_report_actor_columns.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../../migrations/000041_portal_report_actor_columns.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{"ALTER TABLE portal_report_requests", "ALTER TABLE portal_report_grants", "created_by VARCHAR(128)", "updated_by VARCHAR(128)"} {
		if !strings.Contains(text, required) {
			t.Fatalf("actor migration missing %q", required)
		}
	}
	if strings.Count(text, "created_by VARCHAR(128)") != 2 || strings.Count(text, "updated_by VARCHAR(128)") != 2 {
		t.Fatal("up migration must widen both actor columns on exactly both report tables")
	}
	downText := string(down)
	for _, required := range []string{"ALTER TABLE portal_report_requests", "ALTER TABLE portal_report_grants", "created_by VARCHAR(64)", "updated_by VARCHAR(64)", "prove no", "exceeds 64 bytes"} {
		if !strings.Contains(downText, required) {
			t.Fatalf("actor down migration missing %q", required)
		}
	}
	if strings.Count(downText, "created_by VARCHAR(64)") != 2 || strings.Count(downText, "updated_by VARCHAR(64)") != 2 {
		t.Fatal("down migration must narrow both actor columns on exactly both report tables")
	}
}

func TestReportActorModelsMatchPhysicalActorWidth(t *testing.T) {
	for _, model := range []struct {
		name  string
		value any
	}{
		{name: "request", value: Request{}},
		{name: "grant", value: Grant{}},
	} {
		actorModel, ok := reflect.TypeOf(model.value).FieldByName("ActorModel")
		if !ok || !actorModel.Anonymous {
			t.Fatalf("%s must embed ActorModel", model.name)
		}
		createdBy, _ := actorModel.Type.FieldByName("CreatedBy")
		updatedBy, _ := actorModel.Type.FieldByName("UpdatedBy")
		if !strings.Contains(createdBy.Tag.Get("gorm"), "size:128") || !strings.Contains(updatedBy.Tag.Get("gorm"), "size:128") {
			t.Fatalf("%s actor metadata does not match migration 000041", model.name)
		}
	}
}
