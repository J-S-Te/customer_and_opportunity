package presale

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Scripted SQL rows exercise the real MySQL GORM Scan implementation without a
// business database or a new mocking dependency. SQL and bound scope are also
// checked, so the regression cannot pass by replacing repository aggregates.
type reportScanStep struct {
	contains []string
	columns  []string
	values   []driver.Value
	err      error
}

type reportScanScript struct {
	mu    sync.Mutex
	steps []reportScanStep
	next  int
	t     *testing.T
}

type reportScanConnector struct{ script *reportScanScript }

func (c reportScanConnector) Connect(context.Context) (driver.Conn, error) {
	return &reportScanConn{script: c.script}, nil
}
func (c reportScanConnector) Driver() driver.Driver { return reportScanDriver{} }

type reportScanDriver struct{}

func (reportScanDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("only isolated connector is supported")
}

type reportScanConn struct{ script *reportScanScript }

func (*reportScanConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared query")
}
func (*reportScanConn) Close() error { return nil }
func (*reportScanConn) Begin() (driver.Tx, error) {
	return nil, errors.New("read-only aggregate test")
}
func (c *reportScanConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.steps) {
		return nil, errors.New("unexpected aggregate query")
	}
	step := s.steps[s.next]
	s.next++
	for _, fragment := range step.contains {
		if !strings.Contains(query, fragment) {
			s.t.Errorf("query %d missing contract fragment %q", s.next, fragment)
		}
	}
	var tenant, from, to, person, opportunity bool
	for _, arg := range args {
		switch value := arg.Value.(type) {
		case string:
			tenant = tenant || value == "isolated-report-tenant"
			person = person || value == "isolated-report-person"
		case time.Time:
			from = from || value.Equal(reportScanFrom)
			to = to || value.Equal(reportScanFrom.Add(time.Hour))
		case int64:
			opportunity = opportunity || value == 23
		}
	}
	if !tenant || !from || !to || !person || !opportunity {
		s.t.Errorf("query %d lost bound tenant/window/person/opportunity scope", s.next)
	}
	if strings.Contains(query, "FROM crm_outbox_events e") {
		if len(args) < 4 || args[0].Value != "SUCCESS" || args[1].Value != "presale_worklog" || args[2].Value != "isolated-report-tenant" || args[3].Value != "PRESALE_WORKLOG_CREATED" {
			s.t.Error("PMS metrics must use the real outbox type, tenant and successful legacy delivery status")
		}
	}
	if step.err != nil {
		return nil, step.err
	}
	return &reportScanRows{columns: step.columns, values: step.values}, nil
}

type reportScanRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *reportScanRows) Columns() []string { return r.columns }
func (*reportScanRows) Close() error        { return nil }
func (r *reportScanRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

var reportScanFrom = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func reportScanSteps() []reportScanStep {
	return []reportScanStep{
		{contains: []string{"SUM(w.work_hours)", "FROM crm_presale_worklogs w", "w.voided_at IS NULL", "w.work_start>=?", "w.work_start<?", "o.id=?"}, columns: []string{"work_hours", "participant_count", "valid_worklog_count"}, values: []driver.Value{"1.00", int64(1), int64(1)}},
		{contains: []string{"FROM crm_presale_status_logs l", "l.trigger=?", "l.occurred_at>=?"}, columns: []string{"COUNT(DISTINCT r.id)"}, values: []driver.Value{int64(0)}},
		{contains: []string{"FROM crm_opportunities o", "AS active_opportunity_count", "AS covered_opportunity_count"}, columns: []string{"active_opportunity_count", "covered_opportunity_count"}, values: []driver.Value{int64(1), int64(1)}},
		{contains: []string{"FROM crm_outbox_events e", "JOIN crm_presale_worklogs w", "e.aggregate_type=?", "e.aggregate_id=CAST(w.id AS CHAR)", "e.event_type=?"}, columns: []string{"pms_outbox_worklog_count", "pms_success_count"}, values: []driver.Value{int64(0), int64(0)}},
	}
}

func reportScanRepository(t *testing.T, steps []reportScanStep) (*GORMRepository, *reportScanScript) {
	t.Helper()
	script := &reportScanScript{steps: steps, t: t}
	db := sql.OpenDB(reportScanConnector{script: script})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close aggregate connection: %v", err)
		}
	})
	orm, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return NewGORMRepository(orm), script
}

func reportScanQuery() (ReportScope, ReportQuery) {
	return ReportScope{TenantID: "isolated-report-tenant", All: true}, ReportQuery{From: reportScanFrom, To: reportScanFrom.Add(time.Hour), PersonID: "isolated-report-person", OpportunityID: 23}
}

func TestReportSummaryGORMKeepsLocalHoursAndCoverageWithoutPMSOutbox(t *testing.T) {
	repository, script := reportScanRepository(t, reportScanSteps())
	scope, query := reportScanQuery()
	value, err := repository.ReportSummary(context.Background(), scope, query)
	if err != nil {
		t.Fatal(err)
	}
	if script.next != 4 || value.WorkHours != "1.00" || value.ParticipantCount != 1 || value.ValidWorklogCount != 1 || value.CoveredOpportunityCount != 1 || value.ActiveOpportunityCount != 1 || value.AutoCompletedTaskCount != 0 || value.PMSSuccessCount != 0 || value.PMSOutboxWorklogCount != 0 {
		t.Fatalf("local SUCCESS must preserve hours/coverage without becoming a PMS receipt: %+v (queries=%d)", value, script.next)
	}
}

func TestReportSummaryGORMKeepsAllIndependentNonzeroAggregates(t *testing.T) {
	steps := reportScanSteps()
	steps[0].values = []driver.Value{"9.50", int64(3), int64(7)}
	steps[1].values = []driver.Value{int64(2)}
	steps[2].values = []driver.Value{int64(6), int64(4)}
	steps[3].values = []driver.Value{int64(5), int64(3)}
	repository, _ := reportScanRepository(t, steps)
	scope, query := reportScanQuery()
	value, err := repository.ReportSummary(context.Background(), scope, query)
	if err != nil {
		t.Fatal(err)
	}
	if value.WorkHours != "9.50" || value.ParticipantCount != 3 || value.ValidWorklogCount != 7 || value.AutoCompletedTaskCount != 2 || value.ActiveOpportunityCount != 6 || value.CoveredOpportunityCount != 4 || value.PMSOutboxWorklogCount != 5 || value.PMSSuccessCount != 3 {
		t.Fatalf("later GORM scans overwrote independent metrics: %+v", value)
	}
}

func TestReportSummaryGORMPropagatesEachAggregateFailure(t *testing.T) {
	for index := 0; index < 4; index++ {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			steps := reportScanSteps()
			failure := errors.New("isolated aggregate failure")
			steps[index].err = failure
			repository, script := reportScanRepository(t, steps)
			scope, query := reportScanQuery()
			value, err := repository.ReportSummary(context.Background(), scope, query)
			if !errors.Is(err, failure) || value != (ReportSummary{}) || script.next != index+1 {
				t.Fatalf("aggregate %d must stop and propagate failure, not return partial success: value=%+v err=%v queries=%d", index, value, err, script.next)
			}
		})
	}
}

func TestReportSummaryGORMEmptyAggregatesKeepZeroHours(t *testing.T) {
	steps := reportScanSteps()
	steps[0].values = []driver.Value{"0.00", int64(0), int64(0)}
	steps[2].values = []driver.Value{int64(0), int64(0)}
	repository, _ := reportScanRepository(t, steps)
	scope, query := reportScanQuery()
	value, err := repository.ReportSummary(context.Background(), scope, query)
	if err != nil || value.WorkHours != "0.00" || value.ValidWorklogCount != 0 || value.PMSOutboxWorklogCount != 0 {
		t.Fatalf("empty summary must have explicit numeric zero: %+v err=%v", value, err)
	}
}
