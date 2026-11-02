package customer

import (
	"context"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type businessDateRepository struct {
	Repository
	dates []string
}

func (r *businessDateRepository) NextNumber(_ context.Context, _ string, date string) (string, error) {
	r.dates = append(r.dates, date)
	return "KH" + date + "0001", nil
}

func (r *businessDateRepository) Create(context.Context, *Customer) error { return nil }

func TestCustomerNumberUsesBeijingCalendarDate(t *testing.T) {
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
			repository := &businessDateRepository{}
			service := &Service{repo: repository, audit: &createAuditStub{}, now: func() time.Time { return testCase.now }}
			principal := auth.Principal{TenantID: "tenant-a", UserID: "user-a"}
			if err := service.persistCreatedCustomer(context.Background(), principal, &Customer{}, "创建客户"); err != nil {
				t.Fatalf("persistCreatedCustomer() error = %v", err)
			}
			if len(repository.dates) != 1 || repository.dates[0] != testCase.want {
				t.Fatalf("customer number date = %#v, want %q", repository.dates, testCase.want)
			}
		})
	}
}
