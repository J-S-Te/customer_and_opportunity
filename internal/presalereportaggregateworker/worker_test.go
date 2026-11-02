package presalereportaggregateworker

import (
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
)

func TestAggregateWindowIsBeijingCalendarHalfOpen(t *testing.T) {
	// 北京时间 2026-08-02 23:59:59，UTC 已是 2026-08-02 15:59:59。
	now := time.Date(2026, 8, 2, 23, 59, 59, 0, time.FixedZone("CST", 8*60*60))
	window := aggregateWindow(now, 2)
	want := dailyAggregateWindow{
		FromDate:    time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		ToDate:      time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		FromInstant: time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC),
		ToInstant:   time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC),
	}
	if window != want {
		t.Fatalf("window=%+v want=%+v", window, want)
	}
}

// UTC 2025-12-31T16:30Z 已经是北京时间 2026-01-01 00:30，窗口必须属于北京 2026-01-01。
// 旧实现按 UTC 日历日取窗，会把这批工作记到 metric_date=2025-12-31。
func TestAggregateWindowUsesBeijingCalendarAtUTCYearEnd(t *testing.T) {
	now := time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
	want := dailyAggregateWindow{
		FromDate:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ToDate:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		FromInstant: time.Date(2025, 12, 31, 16, 0, 0, 0, time.UTC),
		ToInstant:   time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC),
	}
	if window := aggregateWindow(now, 1); window != want {
		t.Fatalf("window=%+v want=%+v", window, want)
	}
	longer := aggregateWindow(now, 2)
	if !longer.FromDate.Equal(time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)) ||
		!longer.FromInstant.Equal(time.Date(2025, 12, 30, 16, 0, 0, 0, time.UTC)) ||
		!longer.ToInstant.Equal(want.ToInstant) {
		t.Fatalf("lookback=2 window=%+v", longer)
	}
}

// 窗口（work_start 的 UTC 时刻边界 + metric_date 的日期边界）与 SQL 分组必须都用北京日历日。
func TestAggregateWindowAndMetricDateGroupingShareBeijingCalendar(t *testing.T) {
	sql := dailyMetricInsertSQL()
	if strings.Contains(sql, "DATE(w.work_start)") {
		t.Fatalf("aggregate SQL still groups by the UTC calendar day")
	}
	if got := strings.Count(sql, beijingMetricDateExpr); got != 2 {
		t.Fatalf("aggregate SQL uses the Beijing date expression %d times, want select and group by", got)
	}
	if !strings.Contains(sql, beijingMetricDateExpr+" AS metric_date") {
		t.Fatalf("metric_date is not selected from the Beijing date expression")
	}
	if !strings.Contains(sql, "GROUP BY w.tenant_id,"+beijingMetricDateExpr+",o.owner_org_id,w.person_id,o.id") {
		t.Fatalf("group by does not use the same Beijing date expression as metric_date")
	}

	now := time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
	for _, lookback := range []int{1, 2, 32} {
		window := aggregateWindow(now, lookback)
		if days := int(window.ToDate.Sub(window.FromDate) / (24 * time.Hour)); days != lookback {
			t.Fatalf("lookback=%d date span=%d days", lookback, days)
		}
		// 日期边界必须是两个时刻边界各自所在的北京日历日，否则 DATE(?) 的删除范围会错位。
		if !window.FromDate.Equal(beijingDate(window.FromInstant)) || !window.ToDate.Equal(beijingDate(window.ToInstant)) {
			t.Fatalf("lookback=%d window=%+v does not align date and instant bounds", lookback, window)
		}
		if hour, minute, second := window.FromInstant.In(businessclock.Location()).Clock(); hour+minute+second != 0 {
			t.Fatalf("lookback=%d window start is not Beijing midnight: %s", lookback, window.FromInstant)
		}
		// 窗口内每个北京日期的午夜都必须落在 work_start 的 UTC 时刻区间内：不漏日。
		for day := window.FromDate; day.Before(window.ToDate); day = day.AddDate(0, 0, 1) {
			midnight := beijingMidnight(day)
			if midnight.Before(window.FromInstant) || !midnight.Before(window.ToInstant) {
				t.Fatalf("lookback=%d day %s midnight %s falls outside [%s,%s)",
					lookback, day.Format(time.DateOnly), midnight, window.FromInstant, window.ToInstant)
			}
		}
	}
}

// beijingDate 返回 t 所在北京日历日的纯日期（以 UTC 午夜表达，仅供 DATE(?) 比较）。
func beijingDate(t time.Time) time.Time {
	year, month, day := t.In(businessclock.Location()).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// beijingMidnight 把纯日期折算回该北京日历日的 00:00 时刻。
func beijingMidnight(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, businessclock.Location()).UTC()
}

func TestDailyMetricSQLUsesOnlyValidWorklogsAndOutboxEvidence(t *testing.T) {
	sql := dailyMetricInsertSQL()
	for _, fragment := range []string{
		"w.deleted_at IS NULL", "w.voided_at IS NULL", "w.work_start>=?", "w.work_start<?",
		"PRESALE_WORKLOG_CREATED", "w.push_status='SUCCESS'", "GROUP BY w.tenant_id",
		"snapshot.person_name_snapshot", "snapshot.department_snapshot",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("aggregate SQL is missing %q", fragment)
		}
	}
}

func TestLeaseRenewalBindsLiveOwner(t *testing.T) {
	sql := leaseRenewSQL()
	for _, fragment := range []string{"job_name=?", "owner_id=?", "lease_until>=?"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("lease renewal SQL is missing %q", fragment)
		}
	}
}

func TestTenantIdentifierValidationFailsClosed(t *testing.T) {
	for _, value := range []string{"", " tenant-a", "tenant-a ", strings.Repeat("x", 65)} {
		if validateTenantID(value) == nil {
			t.Fatalf("unsafe tenant %q was accepted", value)
		}
	}
	if err := validateTenantID("tenant-a"); err != nil {
		t.Fatal(err)
	}
}
