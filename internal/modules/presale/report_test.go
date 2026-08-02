package presale

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type reportRepositoryStub struct {
	scope ReportScope
	query ReportQuery
}

func (r *reportRepositoryStub) ReportSummary(_ context.Context, scope ReportScope, query ReportQuery) (ReportSummary, error) {
	r.scope, r.query = scope, query
	return ReportSummary{CoveredOpportunityCount: 1, ActiveOpportunityCount: 2, PMSSuccessCount: 3, PMSOutboxWorklogCount: 4}, nil
}
func (r *reportRepositoryStub) ReportTrend(context.Context, ReportScope, ReportQuery) ([]ReportTrendPoint, error) {
	return []ReportTrendPoint{{Date: "2026-08-02", WorkHours: "2.00", ParticipantCount: 1, WorklogCount: 1}}, nil
}
func (r *reportRepositoryStub) ReportDistribution(context.Context, ReportScope, ReportQuery, string) ([]ReportDistributionRow, error) {
	return []ReportDistributionRow{}, nil
}

func reportActor(scope string) Actor {
	return Actor{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1", ScopeMode: scope,
		OrganizationIDs: []string{"org-a"}, Permissions: map[string]bool{"presale.report": true}, Roles: map[string]bool{}}
}

func reportQueryFixture() ReportQuery {
	return ReportQuery{From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60)), To: time.Date(2026, 8, 4, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))}
}

func TestReportSummaryUsesUTCAndPercentUnits(t *testing.T) {
	t.Parallel()
	repo := &reportRepositoryStub{}
	value, err := NewReportService(repo).Summary(context.Background(), reportActor("ALL"), reportQueryFixture())
	if err != nil {
		t.Fatal(err)
	}
	if value.From.Location() != time.UTC || value.From.Hour() != 16 || value.OpportunityCoverageRate != "50.00" || value.PMSSuccessRate != "75.00" {
		t.Fatalf("summary=%+v", value)
	}
}

func TestReportTrendFillsMissingUTCDays(t *testing.T) {
	t.Parallel()
	values, err := NewReportService(&reportRepositoryStub{}).Trend(context.Background(), reportActor("ALL"), reportQueryFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 4 || values[0].Date != "2026-07-31" || values[0].WorkHours != "0.00" || values[2].Date != "2026-08-02" || values[2].WorkHours != "2.00" {
		t.Fatalf("trend=%+v", values)
	}
}

func TestReportScopeAndFiltersCannotExpandOrganizationScope(t *testing.T) {
	t.Parallel()
	service := NewReportService(&reportRepositoryStub{})
	query := reportQueryFixture()
	query.OrganizationID = "org-b"
	if _, err := service.Summary(context.Background(), reportActor("ORG"), query); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Summary() error=%v, want forbidden", err)
	}
	actor := reportActor("ALL")
	actor.Permissions = map[string]bool{}
	if _, err := service.Summary(context.Background(), actor, reportQueryFixture()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Summary() error=%v, want forbidden", err)
	}
	query = reportQueryFixture()
	query.PersonID = "other-person"
	if _, err := service.Summary(context.Background(), reportActor("SELF"), query); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Summary() error=%v, want self filter forbidden", err)
	}
}

func TestReportRejectsInvalidPeriodAndDimension(t *testing.T) {
	t.Parallel()
	service := NewReportService(&reportRepositoryStub{})
	query := reportQueryFixture()
	query.To = query.From
	if _, err := service.Summary(context.Background(), reportActor("ALL"), query); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Summary() error=%v, want invalid", err)
	}
	if _, err := service.Distribution(context.Background(), reportActor("ALL"), reportQueryFixture(), "sql"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Distribution() error=%v, want invalid", err)
	}
}

func TestReportExportFailsClosedAfterAuthorization(t *testing.T) {
	t.Parallel()
	service := NewReportService(&reportRepositoryStub{})
	if err := service.RequestExport(reportActor("ALL"), reportQueryFixture()); !errors.Is(err, ErrReportExportUnavailable) {
		t.Fatalf("RequestExport() error=%v", err)
	}
}

func TestReportSQLKeepsSelfNumeratorAndDenominatorOnParticipationScope(t *testing.T) {
	t.Parallel()
	scope := ReportScope{TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1"}
	query := reportQueryFixture()
	worklogWhere, worklogArgs := reportWorklogWhere(scope, query, "w", "r", "o")
	opportunityWhere, opportunityArgs := reportOpportunityWhere(scope, query, "o")
	coverageWhere, coverageArgs := reportCoverageRequestWhere(scope, query, "r")
	if worklogWhere == "" || opportunityWhere == "" || coverageWhere == "" || len(worklogArgs) != 4 || len(opportunityArgs) != 7 || len(coverageArgs) != 2 {
		t.Fatalf("worklog=%q %#v opportunity=%q %#v", worklogWhere, worklogArgs, opportunityWhere, opportunityArgs)
	}
	if worklogArgs[3] != "person-1" || opportunityArgs[5] != "user-1" || opportunityArgs[6] != "person-1" || coverageArgs[0] != "user-1" || coverageArgs[1] != "person-1" {
		t.Fatalf("self scope mismatch: %#v %#v %#v", worklogArgs, opportunityArgs, coverageArgs)
	}
}

func TestExplicitPersonFilterUsesPeriodWorklogForCoverageNumerator(t *testing.T) {
	t.Parallel()
	query := reportQueryFixture()
	query.PersonID = "person-2"
	where, args := reportCoverageRequestWhere(ReportScope{TenantID: "tenant-1", All: true}, query, "r")
	if !strings.Contains(where, "crm_presale_worklogs") || !strings.Contains(where, "work_start>=?") || len(args) != 3 {
		t.Fatalf("where=%q args=%#v", where, args)
	}
}
