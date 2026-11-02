package opportunity

import (
	"testing"
	"time"
)

// TestEndDateUsesBeijingCalendarDay 锁定 end_date 的口径：它是业务日历日（DATE 列，
// 前端展示“作废日期”），取值按北京日历日派生，并以 UTC 午夜作为 DATE 存储值
// （驱动写库前会按连接 loc=UTC 换算，直接写北京午夜会被写成前一天）。
func TestEndDateUsesBeijingCalendarDay(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			// 北京时间 2026-01-01 00:30，UTC 日期仍是 2025-12-31。
			name: "Beijing midnight moves end date to the new day",
			now:  time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC),
			want: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// 北京时间 2025-12-31 23:30，仍归属 12-31。
			name: "Beijing late evening keeps the same day",
			now:  time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
			want: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			// 已经带北京时区的时刻不再二次换算。
			name: "Beijing zoned instant is not shifted twice",
			now:  time.Date(2026, 3, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
			want: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := businessDateValue(testCase.now); !got.Equal(testCase.want) {
				t.Fatalf("businessDateValue(%s) = %s, want %s", testCase.now, got, testCase.want)
			}
			// 落库往返后仍是同一个北京日历日，响应里的 end_date 不得偏移。
			endDate := businessDateValue(testCase.now)
			model := &Opportunity{Status: StatusVoid, EndDate: &endDate}
			if response := toResponse(model); response.EndDate == nil || *response.EndDate != testCase.want.Format("2006-01-02") {
				t.Fatalf("end_date = %v, want %s", response.EndDate, testCase.want.Format("2006-01-02"))
			}
		})
	}
}
