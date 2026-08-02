package presalereportaggregateworker

import (
	"strings"
	"testing"
	"time"
)

func TestAggregateWindowIsUTCHalfOpenAndBounded(t *testing.T) {
	now := time.Date(2026, 8, 2, 23, 59, 59, 0, time.FixedZone("CST", 8*60*60))
	from, to := aggregateWindow(now, 2)
	wantFrom := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) || !to.Equal(wantTo) || from.Location() != time.UTC || to.Location() != time.UTC {
		t.Fatalf("window=[%s,%s) want=[%s,%s)", from, to, wantFrom, wantTo)
	}
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
