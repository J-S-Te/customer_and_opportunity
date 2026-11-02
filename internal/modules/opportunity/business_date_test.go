package opportunity

import (
	"context"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type businessDateOpportunityRepository struct {
	*createIdempotencyRepository
	dates []string
}

func (r *businessDateOpportunityRepository) NextNumber(ctx context.Context, tenant, date string) (string, error) {
	r.dates = append(r.dates, date)
	return r.createIdempotencyRepository.NextNumber(ctx, tenant, date)
}

func TestOpportunityNumberUsesBeijingCalendarDate(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{
			// 北京时间 2026-01-01 00:30，UTC 日期仍是 2025-12-31。
			name: "Beijing midnight crosses the year boundary",
			now:  time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC),
			want: "20260101",
		},
		{
			// 北京时间 2025-12-31 23:30。
			name: "Beijing late evening keeps the old day",
			now:  time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
			want: "20251231",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &businessDateOpportunityRepository{
				createIdempotencyRepository: &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true},
			}
			service := createTestService(repository, &countingAuditWriter{})
			service.now = func() time.Time { return testCase.now }
			principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
			if _, err := service.Create(createTestContext(principal), createTestInput()); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if len(repository.dates) != 1 || repository.dates[0] != testCase.want {
				t.Fatalf("opportunity number date = %#v, want %q", repository.dates, testCase.want)
			}
		})
	}
}
