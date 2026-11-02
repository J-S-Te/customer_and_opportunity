package opportunity

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// TestCreateRejectsExpectedSignDateBeforeBeijingToday 锁定“预计签约日不早于今天”
// 按北京日历日判定：北京时间 2026-01-01 00:30（UTC 仍是 2025-12-31）时，
// 2025-12-31 业务上已是昨天，必须被拒。
func TestCreateRejectsExpectedSignDateBeforeBeijingToday(t *testing.T) {
	// 北京时间 2026-01-01 00:30。
	beijingNewYearFirstMinutes := time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
	cases := []struct {
		name         string
		signDate     string
		now          time.Time
		wantRejected bool
	}{
		{
			name:         "Beijing first minutes of the new year reject yesterday",
			signDate:     "2025-12-31",
			now:          beijingNewYearFirstMinutes,
			wantRejected: true,
		},
		{
			name:     "Beijing today is accepted",
			signDate: "2026-01-01",
			now:      beijingNewYearFirstMinutes,
		},
		{
			// 北京时间 2025-12-31 23:30（UTC 2025-12-31T15:30）业务上仍是 12-31 当天。
			name:     "Beijing late evening still accepts the same calendar day",
			signDate: "2025-12-31",
			now:      time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
		},
		{
			name:         "day before Beijing today is rejected in the evening",
			signDate:     "2025-12-30",
			now:          time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
			wantRejected: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
			repository := &createIdempotencyRepository{GORMRepository: &GORMRepository{}, visible: true}
			service := createTestService(repository, &countingAuditWriter{})
			service.now = func() time.Time { return testCase.now }
			input := createTestInput()
			input.ExpectedSignDate = testCase.signDate
			_, err := service.Create(createTestContext(principal), input)
			if !testCase.wantRejected {
				if err != nil {
					t.Fatalf("Create() error = %v, want accepted", err)
				}
				return
			}
			appErr := apperror.As(err)
			if appErr == nil || appErr.Code != "CRM_OPPORTUNITY_INVALID_SIGN_DATE" || appErr.HTTPStatus != 422 {
				t.Fatalf("Create() error = %#v, want 422 CRM_OPPORTUNITY_INVALID_SIGN_DATE", appErr)
			}
			if repository.created != 0 {
				t.Fatalf("created = %d, want 0 (validation must happen before persistence)", repository.created)
			}
		})
	}
}

// TestSignDateBeforeCreationUsesBeijingCalendarDay 锁定 Update 的“签约日不早于商机创建日”
// 与 Create 同口径：两侧都折算成北京日历日再比。旧实现用 UTC 日粒度的 dateOnly，
// 北京时间 00:00–08:00 创建的商机会被算成 UTC 的前一天，从而错放签约日。
func TestSignDateBeforeCreationUsesBeijingCalendarDay(t *testing.T) {
	// 商机创建于北京时间 2026-01-01 00:30（UTC 仍是 2025-12-31）。
	beijingNewYearCreation := time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
	cases := []struct {
		name      string
		createdAt time.Time
		signDate  string
		want      bool
	}{
		{
			name:      "Beijing creation day rejects the previous UTC day",
			createdAt: beijingNewYearCreation,
			signDate:  "2025-12-31",
			want:      true,
		},
		{
			name:      "Beijing creation day accepts the same Beijing day",
			createdAt: beijingNewYearCreation,
			signDate:  "2026-01-01",
		},
		{
			// 北京时间 2025-12-31 23:30 创建，UTC 日仍是 12-31，两侧同日放行。
			name:      "Beijing late evening creation accepts the same calendar day",
			createdAt: time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC),
			signDate:  "2025-12-31",
		},
		{
			// 闸门只比“商机创建日”，不比“今天”：创建日已过去很久时，晚于创建日的签约日仍放行。
			name:      "gate compares creation day instead of today",
			createdAt: time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC),
			signDate:  "2025-06-01",
		},
		{
			name:      "zero creation timestamp skips the gate",
			createdAt: time.Time{},
			signDate:  "2000-01-01",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signDate, err := time.Parse("2006-01-02", testCase.signDate)
			if err != nil {
				t.Fatalf("parse sign date: %v", err)
			}
			if got := signDateBeforeCreation(testCase.createdAt, signDate); got != testCase.want {
				t.Fatalf("signDateBeforeCreation(%s, %s) = %t, want %t", testCase.createdAt, testCase.signDate, got, testCase.want)
			}
		})
	}
}

// TestUpdateRejectsSignDateBeforeBeijingCreationDay 通过 Update 入口验证北京凌晨边界：
// 商机创建于 UTC 2025-12-31T16:30Z（北京 2026-01-01 00:30），签约日 2025-12-31 必须被拒；
// 2026-01-01 则放行，放行后先撞到版本冲突（版本校验在签约日闸门之后），
// 两种口径都在事务之前返回，不依赖数据库。
func TestUpdateRejectsSignDateBeforeBeijingCreationDay(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}
	newRepository := func() *externalEdgesRepository {
		model := &Opportunity{
			OpportunityNo: "SJ202601010001", CustomerID: 3, ExpectedAmount: decimal.RequireFromString("100.00"),
			ExpectedSignDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CurrentStage: StageRequirement, Status: StatusFollowing,
		}
		model.ID, model.TenantID, model.Version = 7, "tenant-a", 7
		// 创建于北京时间 2026-01-01 00:30（UTC 仍是 2025-12-31）。
		model.CreatedAt = time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)
		return &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: model}
	}
	updateInput := func(signDate string, version uint64) UpdateRequest {
		return UpdateRequest{Name: "商机", Type: "软件", Source: "转介绍", ExpectedAmount: "100.00", ExpectedSignDate: signDate, RequirementSummary: "需求", Version: version, Reason: "改签约日"}
	}

	repository := newRepository()
	writer := &countingAuditWriter{}
	service := externalTestService(repository)
	service.audit = writer
	_, err := service.Update(createTestContext(principal), 7, updateInput("2025-12-31", 8))
	appErr := apperror.As(err)
	if appErr == nil || appErr.Code != "CRM_OPPORTUNITY_INVALID_SIGN_DATE" || appErr.HTTPStatus != 422 {
		t.Fatalf("Update() error = %#v, want 422 CRM_OPPORTUNITY_INVALID_SIGN_DATE", appErr)
	}
	if writer.writes != 0 {
		t.Fatalf("audit writes = %d, want 0 (validation must happen before persistence)", writer.writes)
	}

	repository = newRepository()
	service = externalTestService(repository)
	if _, err = service.Update(createTestContext(principal), 7, updateInput("2026-01-01", 8)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("Update() error = %v, want %v (sign date gate must accept the Beijing creation day)", err, ErrVersionConflict)
	}
}
