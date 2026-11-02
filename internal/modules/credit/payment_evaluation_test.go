package credit

import (
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/businessclock"
)

// TestPaymentEvaluationComparesBeijingCalendarDays 锁定宽限期按北京日历日判定：
// 宽限截止日当天仍为 ONTIME，次日起为 LATE；同时覆盖北京时间 00:00–08:00
// 折算到 UTC 前一天的边界。到期日是业务日期，读出来按 UTC 午夜表示。
func TestPaymentEvaluationComparesBeijingCalendarDays(t *testing.T) {
	// 到期日 2026-01-05 的业务日期，数据库 DATE 列读出后是 UTC 午夜。
	dueDate := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		due   time.Time
		paid  time.Time
		grace int
		want  string
	}{
		{
			// GraceDays=7 时宽限截止日为 2026-01-12，当天北京时间 23:59 仍算准时。
			name:  "grace day itself is ONTIME",
			paid:  time.Date(2026, 1, 12, 23, 59, 0, 0, businessclock.Location()),
			grace: 7,
			want:  "ONTIME",
		},
		{
			name:  "day after grace day is LATE",
			paid:  time.Date(2026, 1, 13, 0, 0, 1, 0, businessclock.Location()),
			grace: 7,
			want:  "LATE",
		},
		{
			// 北京时间 2026-01-12 00:30 折算成 UTC 还是 2026-01-11T16:30。
			name:  "grace day early Beijing morning is ONTIME",
			paid:  time.Date(2026, 1, 11, 16, 30, 0, 0, time.UTC),
			grace: 7,
			want:  "ONTIME",
		},
		{
			// 北京时间 2026-01-13 00:30，UTC 仍是 2026-01-12T16:30。
			name:  "day after grace day early Beijing morning is LATE",
			paid:  time.Date(2026, 1, 12, 16, 30, 0, 0, time.UTC),
			grace: 7,
			want:  "LATE",
		},
		{
			name:  "grace zero due day is ONTIME",
			paid:  time.Date(2026, 1, 5, 23, 30, 0, 0, businessclock.Location()),
			grace: 0,
			want:  "ONTIME",
		},
		{
			name:  "grace zero next day is LATE",
			paid:  time.Date(2026, 1, 6, 0, 30, 0, 0, businessclock.Location()),
			grace: 0,
			want:  "LATE",
		},
		{
			// 到期日按北京午夜表示（北京时间 2026-01-05 00:30 = UTC 2026-01-04T16:30），
			// 业务日仍是 2026-01-05，不得被折算到前一天。
			name:  "due date expressed in Beijing midnight keeps its business day",
			due:   time.Date(2026, 1, 4, 16, 30, 0, 0, time.UTC),
			paid:  time.Date(2026, 1, 5, 23, 0, 0, 0, businessclock.Location()),
			grace: 0,
			want:  "ONTIME",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			due := testCase.due
			if due.IsZero() {
				due = dueDate
			}
			if got := paymentEvaluation(due, testCase.paid, testCase.grace); got != testCase.want {
				t.Fatalf("paymentEvaluation(due=%s, paid=%s, grace=%d) = %q, want %q",
					due, testCase.paid, testCase.grace, got, testCase.want)
			}
		})
	}
}
