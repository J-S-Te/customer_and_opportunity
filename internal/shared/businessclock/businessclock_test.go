package businessclock

import (
	"testing"
	"time"
)

func TestDateStringUsesBeijingCalendarAcrossYearBoundary(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{
			// 北京时间 2026-01-01 00:30，UTC 仍是 2025-12-31。
			name: "Beijing midnight belongs to the new year",
			at:   time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC),
			want: "20260101",
		},
		{
			// 北京时间 2025-12-31 23:30。
			name: "Beijing late evening stays in the old year",
			at:   time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
			want: "20251231",
		},
		{
			name: "instant already expressed in Beijing time",
			at:   time.Date(2026, 1, 1, 0, 30, 0, 0, Location()),
			want: "20260101",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DateString(testCase.at); got != testCase.want {
				t.Fatalf("DateString(%s) = %q, want %q", testCase.at, got, testCase.want)
			}
		})
	}
}

func TestLocationIsFixedBeijingOffset(t *testing.T) {
	if _, offset := time.Now().In(Location()).Zone(); offset != 8*60*60 {
		t.Fatalf("Location() offset = %d seconds, want %d", offset, 8*60*60)
	}
}

func TestNowIsExpressedInBeijingTime(t *testing.T) {
	now := Now()
	if now.Location() != Location() {
		t.Fatalf("Now() location = %v, want %v", now.Location(), Location())
	}
	if _, offset := now.Zone(); offset != 8*60*60 {
		t.Fatalf("Now() offset = %d seconds, want %d", offset, 8*60*60)
	}
}
