package opportunity

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func closedOpportunity(stage string, version uint64) *Opportunity {
	model := signedOpportunity(version)
	model.CurrentStage = stage
	model.StageChangedAt = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	if stage == StageFailed {
		model.ContractRef = nil
		reason := "价格"
		model.LostReason = &reason
	}
	return model
}

func TestChangeStageRejectsClosedOpportunity(t *testing.T) {
	for _, stage := range []string{StageSigned, StageFailed} {
		t.Run(stage, func(t *testing.T) {
			repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: closedOpportunity(stage, 7)}
			writer := &countingAuditWriter{}
			service := externalTestService(repo)
			service.audit = writer
			_, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, StageChangeRequest{Version: 7, TargetStage: StageRequirement, Reason: "回退"})
			if !errors.Is(err, ErrTerminalStageLocked) {
				t.Fatalf("closed opportunity stage change error=%v, want %v", err, ErrTerminalStageLocked)
			}
			if repo.stageUpdates != 0 || repo.stageLogs != 0 || writer.writes != 0 {
				t.Fatalf("closed opportunity stage change mutated state: repo=%#v audit=%#v", repo, writer)
			}
		})
	}
}

func TestChangeStageKeepsVoidSemantics(t *testing.T) {
	model := signedOpportunity(7)
	model.Status = StatusVoid
	repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: model}
	service := externalTestService(repo)
	_, err := service.ChangeStage(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, StageChangeRequest{Version: 7, TargetStage: StageRequirement, Reason: "回退"})
	if !errors.Is(err, ErrInactive) {
		t.Fatalf("void opportunity stage change error=%v, want %v", err, ErrInactive)
	}
}

func TestAllowedStageTransitionsKeepInProgressMoves(t *testing.T) {
	inProgress := []string{StageInitial, StageRequirement, StageSolution, StageQuotation, StageBid}
	for _, from := range inProgress {
		for _, to := range inProgress {
			if !allowedStageTransitions[from][to] {
				t.Fatalf("in-progress transition %q -> %q was blocked", from, to)
			}
		}
		for _, to := range []string{StageSigned, StageFailed} {
			if !allowedStageTransitions[from][to] {
				t.Fatalf("terminal transition %q -> %q was blocked", from, to)
			}
		}
	}
	for _, from := range []string{StageSigned, StageFailed} {
		if len(allowedStageTransitions[from]) != 0 {
			t.Fatalf("terminal stage %q must not allow any transition: %#v", from, allowedStageTransitions[from])
		}
	}
}

func TestValidateStageChangeRules(t *testing.T) {
	following := &Opportunity{CurrentStage: StageQuotation, Status: StatusFollowing}
	for _, target := range []string{StageInitial, StageRequirement, StageSolution, StageQuotation, StageBid, StageSigned, StageFailed} {
		if err := validateStageChange(following, target); err != nil {
			t.Fatalf("following opportunity -> %q rejected: %v", target, err)
		}
	}
	if err := validateStageChange(&Opportunity{CurrentStage: StageSigned, Status: StatusClosed}, StageRequirement); !errors.Is(err, ErrTerminalStageLocked) {
		t.Fatalf("closed opportunity error=%v", err)
	}
	if err := validateStageChange(&Opportunity{CurrentStage: StageSigned, Status: StatusVoid}, StageRequirement); !errors.Is(err, ErrInactive) {
		t.Fatalf("void opportunity error=%v", err)
	}
	if err := validateStageChange(&Opportunity{CurrentStage: "未知阶段", Status: StatusFollowing}, StageQuotation); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("unknown source stage error=%v", err)
	}
}

func TestUpdateFreezesAmountAndSignDateAfterClose(t *testing.T) {
	for _, stage := range []string{StageSigned, StageFailed} {
		t.Run(stage, func(t *testing.T) {
			repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: closedOpportunity(stage, 7)}
			service := externalTestService(repo)
			input := UpdateRequest{Name: "商机", Type: "软件", Source: "转介绍", ExpectedAmount: "200.00", ExpectedSignDate: "2026-09-30", RequirementSummary: "需求", Version: 7, Reason: "改金额"}
			if _, err := service.Update(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, input); !errors.Is(err, ErrTerminalMasterDataFrozen) {
				t.Fatalf("amount change after close error=%v, want %v", err, ErrTerminalMasterDataFrozen)
			}
			input.ExpectedAmount = "100.00"
			input.ExpectedSignDate = "2026-09-01"
			if _, err := service.Update(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "actor-a"}), 7, input); !errors.Is(err, ErrTerminalMasterDataFrozen) {
				t.Fatalf("sign date change after close error=%v, want %v", err, ErrTerminalMasterDataFrozen)
			}
		})
	}
}

func TestClosedMasterDataComparisonIgnoresDateFormatting(t *testing.T) {
	model := closedOpportunity(StageSigned, 7)
	model.ExpectedAmount = decimal.RequireFromString("100.00")
	model.ExpectedSignDate = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	if closedMasterDataChanged(model, decimal.RequireFromString("100"), time.Date(2026, 8, 31, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))) {
		t.Fatal("unchanged amount and business date must stay editable after close")
	}
	if !closedMasterDataChanged(model, decimal.RequireFromString("100.01"), model.ExpectedSignDate) {
		t.Fatal("amount change after close was not detected")
	}
	if !closedMasterDataChanged(model, model.ExpectedAmount, model.ExpectedSignDate.AddDate(0, 0, 1)) {
		t.Fatal("sign date change after close was not detected")
	}
}

func TestExternalStatusCannotReviveClosedOpportunity(t *testing.T) {
	tests := []struct {
		name   string
		stage  string
		status string
	}{
		{name: "signed opportunity rejects late quotation status", stage: StageSigned, status: "报价已通过"},
		{name: "failed opportunity rejects late bid win", stage: StageFailed, status: "投标中标"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := closedOpportunity(test.stage, 7)
			repo := &externalEdgesRepository{GORMRepository: &GORMRepository{}, opportunity: model}
			writer := &auditCapture{}
			service := externalTestService(repo)
			service.audit = writer
			_, err := service.ApplyExternalStatus(createTestContext(auth.Principal{TenantID: "tenant-a", UserID: "machine-a"}), ExternalStatusRequest{OpportunityID: 7, SourceID: "BJ-1", Status: test.status, ChangedAt: model.StageChangedAt.Add(time.Minute)})
			if !errors.Is(err, ErrExternalStatusTerminal) {
				t.Fatalf("terminal external callback error=%v, want %v", err, ErrExternalStatusTerminal)
			}
			if repo.stageUpdates != 0 || repo.externalCreates != 0 || repo.stageLogs != 0 {
				t.Fatalf("terminal external callback mutated state: %#v", repo)
			}
			if writer.writes != 1 || writer.operation != "EXTERNAL_STATUS_TERMINAL_REJECTED" || writer.result != "REJECTED" {
				t.Fatalf("terminal external callback audit=%#v", writer)
			}
			if repo.opportunity.Status != StatusClosed || repo.opportunity.CurrentStage != test.stage {
				t.Fatalf("closed opportunity was revived: %#v", repo.opportunity)
			}
		})
	}
}
