package presale

import (
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func presaleDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPresaleQueryMigrationAddsAndRemovesExpectedEndIndex(t *testing.T) {
	t.Parallel()
	up, err := os.ReadFile("../../../migrations/000037_presale_query_indexes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000037_presale_query_indexes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"idx_presale_expected_status", "tenant_id,expected_end,status,id", "No row backfill is required", "online-DDL"} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	if !strings.Contains(string(down), "DROP INDEX idx_presale_expected_status") {
		t.Fatal("down migration does not remove expected-end index")
	}
}

func TestSharedRequestQueryBindsTenantHistoricalScopeAndAllFilters(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	to := now.Add(24 * time.Hour)
	overdue := true
	scope := RequestQueryScope{TenantID: "tenant-a", ApplicantID: "sales-a", AssigneeID: "person-a"}
	query := RequestListQuery{
		RequestNo: "TS%_literal", OpportunityID: 7, ApplicantID: "applicant-a", AssigneeID: "assignee-a",
		Status: StatusExecuting, Venue: VenueRemote, Urgency: UrgencyUrgent,
		CreatedFrom: &now, CreatedTo: &to, ExpectedFrom: &now, ExpectedTo: &to,
		Overdue: &overdue, PushStatus: PushRetryWait,
	}
	statement := applyRequestFilters(applyRequestScope(presaleDryRunDB(t).Model(&PresaleRequest{}), scope), query, now).
		Find(&[]PresaleRequest{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{
		"crm_presale_requests.tenant_id", "scope_assignment.assignee_id", "INSTR(crm_presale_requests.request_no",
		"crm_presale_requests.opportunity_id", "crm_presale_requests.applicant_id", "filter_assignment.assignee_id",
		"crm_presale_requests.status", "crm_presale_requests.venue", "crm_presale_requests.urgency",
		"crm_presale_requests.created_at>=", "crm_presale_requests.created_at<", "crm_presale_requests.expected_end>=",
		"crm_presale_requests.expected_end<", "crm_presale_requests.status NOT IN", "filter_worklog.push_status",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing %q in %q", fragment, sql)
		}
	}
	for _, value := range []any{"tenant-a", "sales-a", "person-a", "TS%_literal", uint64(7), "applicant-a", "assignee-a", StatusExecuting, VenueRemote, UrgencyUrgent, PushRetryWait} {
		found := false
		for _, bound := range statement.Vars {
			if bound == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("bound value %#v absent from %#v", value, statement.Vars)
		}
	}
}

func TestAllScopeStillBindsTenant(t *testing.T) {
	t.Parallel()
	statement := applyRequestScope(presaleDryRunDB(t).Model(&PresaleRequest{}), RequestQueryScope{TenantID: "tenant-a", All: true}).
		Find(&[]PresaleRequest{}).Statement
	if !strings.Contains(statement.SQL.String(), "crm_presale_requests.tenant_id") || len(statement.Vars) != 1 || statement.Vars[0] != "tenant-a" {
		t.Fatalf("sql=%q vars=%#v", statement.SQL.String(), statement.Vars)
	}
}

func TestRoleSpecificRequestScopesFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		scope     RequestQueryScope
		contains  []string
		forbidden []string
	}{
		{
			name: "sales applicant only", scope: RequestQueryScope{TenantID: "tenant-a", ApplicantID: "sales-a"},
			contains: []string{"crm_presale_requests.applicant_id"}, forbidden: []string{"scope_assignment.assignee_id", "1=0"},
		},
		{
			name: "technician assignment only", scope: RequestQueryScope{TenantID: "tenant-a", AssigneeID: "person-a"},
			contains: []string{"scope_assignment.assignee_id"}, forbidden: []string{"crm_presale_requests.applicant_id", "1=0"},
		},
		{
			name: "missing authorized relation", scope: RequestQueryScope{TenantID: "tenant-a"},
			contains: []string{"1=0"}, forbidden: []string{"crm_presale_requests.applicant_id", "scope_assignment.assignee_id"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			statement := applyRequestScope(presaleDryRunDB(t).Model(&PresaleRequest{}), test.scope).
				Find(&[]PresaleRequest{}).Statement
			sql := statement.SQL.String()
			for _, fragment := range test.contains {
				if !strings.Contains(sql, fragment) {
					t.Fatalf("missing %q in %q", fragment, sql)
				}
			}
			for _, fragment := range test.forbidden {
				if strings.Contains(sql, fragment) {
					t.Fatalf("unexpected %q in %q", fragment, sql)
				}
			}
		})
	}
}
