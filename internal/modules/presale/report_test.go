package presale

import (
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"
)

type reportRepositoryStub struct {
	scope ReportScope
	query ReportQuery
	rows  []ReportDistributionRow
	err   error
}

func (r *reportRepositoryStub) ReportSummary(_ context.Context, scope ReportScope, query ReportQuery) (ReportSummary, error) {
	r.scope, r.query = scope, query
	return ReportSummary{CoveredOpportunityCount: 1, ActiveOpportunityCount: 2, PMSSuccessCount: 3, PMSOutboxWorklogCount: 4}, nil
}
func (r *reportRepositoryStub) ReportTrend(context.Context, ReportScope, ReportQuery) ([]ReportTrendPoint, error) {
	return []ReportTrendPoint{{Date: "2026-08-02", WorkHours: "2.00", ParticipantCount: 1, WorklogCount: 1}}, nil
}
func (r *reportRepositoryStub) ReportDistribution(_ context.Context, scope ReportScope, query ReportQuery, _ string) ([]ReportDistributionRow, error) {
	r.scope, r.query = scope, query
	return r.rows, r.err
}
func (r *reportRepositoryStub) ReportFilterOptions(context.Context, ReportScope, ReportQuery) (RequestFilterOptions, error) {
	return RequestFilterOptions{}, nil
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

func TestReportTrendFillsMissingBeijingDays(t *testing.T) {
	t.Parallel()
	// 夹具窗口是北京 [2026-08-01 00:00, 2026-08-04 00:00)，归一后是 UTC
	// [2026-07-31 16:00, 2026-08-03 16:00)；趋势按北京日历日打标签，因此是 3 天。
	values, err := NewReportService(&reportRepositoryStub{}).Trend(context.Background(), reportActor("ALL"), reportQueryFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || values[0].Date != "2026-08-01" || values[0].WorkHours != "0.00" || values[1].Date != "2026-08-02" || values[1].WorkHours != "2.00" || values[2].Date != "2026-08-03" {
		t.Fatalf("trend=%+v", values)
	}
}

// 跨月边界：UTC 2025-12-31T16:00Z 起已是北京 2026-01-01，横轴不能出现 2025-12-31。
func TestReportTrendUsesBeijingDaysAcrossUTCMonthBoundary(t *testing.T) {
	t.Parallel()
	query := ReportQuery{
		From: time.Date(2025, 12, 31, 16, 0, 0, 0, time.UTC), // 北京 2026-01-01 00:00
		To:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),    // 北京 2026-01-02 08:00
	}
	values, err := NewReportService(&reportRepositoryStub{}).Trend(context.Background(), reportActor("ALL"), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Date != "2026-01-01" || values[1].Date != "2026-01-02" {
		t.Fatalf("trend=%+v", values)
	}
	// 同一窗口内的任一时刻，其北京日期都必须落在补零范围内，否则 SQL 返回的行会被丢弃。
	first, endExclusive := trendDayRange(query.From, query.To)
	for at := query.From; at.Before(query.To); at = at.Add(15 * time.Minute) {
		day := beijingDay(at)
		if day.Before(first) || !day.Before(endExclusive) {
			t.Fatalf("instant %s maps to Beijing day %s outside [%s,%s)",
				at, day.Format(time.DateOnly), first.Format(time.DateOnly), endExclusive.Format(time.DateOnly))
		}
	}
}

// To 恰为北京午夜时该日不进横轴；越过后一纳秒才进横轴，保证 [From,To) 与分组一致。
func TestReportTrendDayRangeTreatsUpperBoundAsExclusive(t *testing.T) {
	t.Parallel()
	from := time.Date(2025, 12, 31, 16, 0, 0, 0, time.UTC) // 北京 2026-01-01 00:00
	to := time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC)     // 北京 2026-01-02 00:00
	first, endExclusive := trendDayRange(from, to)
	if first.Format(time.DateOnly) != "2026-01-01" || endExclusive.Format(time.DateOnly) != "2026-01-02" {
		t.Fatalf("day range=[%s,%s) want=[2026-01-01,2026-01-02)", first.Format(time.DateOnly), endExclusive.Format(time.DateOnly))
	}
	if _, extended := trendDayRange(from, to.Add(time.Nanosecond)); extended.Format(time.DateOnly) != "2026-01-03" {
		t.Fatalf("day range end=%s want=2026-01-03", extended.Format(time.DateOnly))
	}
}

// 分组 SQL 与补零范围必须同用北京日历日；只改一端会造成漏日或重日。
func TestReportTrendSQLGroupsByBeijingCalendarDay(t *testing.T) {
	t.Parallel()
	where, _ := reportWorklogWhere(ReportScope{TenantID: "tenant-1", All: true}, reportQueryFixture(), "w", "r", "o")
	sql := reportTrendSQL(where)
	if strings.Contains(sql, "DATE_FORMAT(w.work_start") {
		t.Fatalf("trend SQL still groups by the UTC calendar day")
	}
	if got := strings.Count(sql, beijingWorklogDayExpr); got != 3 {
		t.Fatalf("trend SQL uses the Beijing day expression %d times, want select/group by/order by", got)
	}
	if !strings.Contains(sql, beijingWorklogDayExpr+" AS date") ||
		!strings.Contains(sql, "GROUP BY "+beijingWorklogDayExpr) ||
		!strings.Contains(sql, "ORDER BY "+beijingWorklogDayExpr) {
		t.Fatalf("trend SQL does not use the same Beijing day expression everywhere")
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

func TestReportExportContainsSummaryTrendDistributionAndEscapesFormulas(t *testing.T) {
	t.Parallel()
	repo := &reportRepositoryStub{rows: []ReportDistributionRow{{DimensionName: " =HYPERLINK(\"bad\")", Department: "\t@SUM(1)", WorkHours: "2.50"}}}
	data, err := NewReportService(repo).RequestExport(context.Background(), reportActor("ORG"), reportQueryFixture(), "PERSON")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "\xef\xbb\xbf") {
		t.Fatal("missing Excel UTF-8 BOM")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\xef\xbb\xbf")))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[3][7] != "50.00" || rows[len(rows)-1][1] != "' =HYPERLINK(\"bad\")" || rows[len(rows)-1][2] != "'\t@SUM(1)" || repo.scope.All || repo.scope.OrganizationIDs[0] != "org-a" || repo.query.distributionLimit != 10001 {
		t.Fatalf("rows=%v scope=%+v query=%+v", rows, repo.scope, repo.query)
	}
}

func TestReportExportRejectsUnauthorizedInvalidDimensionsAndLimit(t *testing.T) {
	t.Parallel()
	repo := &reportRepositoryStub{}
	service := NewReportService(repo)
	actor := reportActor("ALL")
	actor.Permissions = nil
	if _, err := service.RequestExport(context.Background(), actor, reportQueryFixture(), "PERSON"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.RequestExport(context.Background(), reportActor("ALL"), reportQueryFixture(), "SQL"); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	repo.rows = make([]ReportDistributionRow, 10001)
	if _, err := service.RequestExport(context.Background(), reportActor("ALL"), reportQueryFixture(), "PERSON"); !errors.Is(err, ErrReportExportLimit) {
		t.Fatal(err)
	}
	repo.rows = nil
	repo.err = errors.New("db unavailable")
	if _, err := service.RequestExport(context.Background(), reportActor("ALL"), reportQueryFixture(), "PERSON"); !errors.Is(err, repo.err) {
		t.Fatal(err)
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
