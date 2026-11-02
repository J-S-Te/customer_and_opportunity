package presale

import (
	"context"
	"testing"
	"time"
)

// businessDateRequestRepository 复用既有创建测试桩，仅拦截服务层时钟传入的时刻，
// 用生产代码的 sequenceDate 生成单号日期段，模拟 GORMRepository.nextNumber 的换算。
type businessDateRequestRepository struct {
	createSecurityRepository
	dates []string
}

func (r *businessDateRequestRepository) NextRequestNo(_ context.Context, _ string, at time.Time) (string, error) {
	date := sequenceDate(at)
	r.dates = append(r.dates, date)
	return "TS" + date + "0001", nil
}

func (r *businessDateRequestRepository) CreateRequest(_ context.Context, value *PresaleRequest) error {
	value.ID = 1
	return nil
}

func (r *businessDateRequestRepository) CreateApprovalInstance(context.Context, *ApprovalInstance) error {
	return nil
}

func TestPresaleRequestNumberUsesBeijingCalendarDate(t *testing.T) {
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
			repository := &businessDateRequestRepository{}
			opportunities := &accessibleOpportunityReader{values: map[uint64]OpportunitySnapshot{7: {ID: 7, OpportunityNo: "SJ202512310001"}}}
			service := NewService(repository, opportunities, testPhoneProtector{}, fixedClock{at: testCase.now}, fixedIDs{}).UseAuditWriter(&requestAuditRecorder{})
			actor := Actor{TenantID: "tenant-1", UserID: "sales-a", UserName: "Sales A", Permissions: map[string]bool{"presale.create": true}}
			created, err := service.CreateRequest(context.Background(), actor, "beijing-business-date", validCreateSecurityInput(7))
			if err != nil {
				t.Fatalf("CreateRequest() error = %v", err)
			}
			if len(repository.dates) != 1 || repository.dates[0] != testCase.want {
				t.Fatalf("presale request number date = %#v, want %q", repository.dates, testCase.want)
			}
			if created.RequestNo != "TS"+testCase.want+"0001" {
				t.Fatalf("request number = %q, want date segment %q", created.RequestNo, testCase.want)
			}
		})
	}
}
