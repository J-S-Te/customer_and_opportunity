package opportunity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestValidateMasterData(t *testing.T) {
	amount, signDate, err := validateMasterData("120000.50", "2026-08-31")
	if err != nil || amount.StringFixed(2) != "120000.50" || signDate.Format("2006-01-02") != "2026-08-31" {
		t.Fatalf("valid master data rejected: amount=%s date=%v err=%v", amount, signDate, err)
	}
	for _, test := range []struct{ amount, date string }{{"0", "2026-08-31"}, {"bad", "2026-08-31"}, {"10", "bad"}} {
		if _, _, err = validateMasterData(test.amount, test.date); err == nil {
			t.Fatalf("invalid master data accepted: %#v", test)
		}
	}
}

func TestOpportunityMasterDataRejectsWhitespace(t *testing.T) {
	if err := validateOpportunityMasterData("商机", "软件", "转介绍", "需求说明"); err != nil {
		t.Fatalf("valid opportunity data rejected: %v", err)
	}
	if err := validateOpportunityMasterData(" ", "软件", "转介绍", "需求说明"); err == nil {
		t.Fatal("blank opportunity name accepted")
	}
	if err := validateOpportunityMasterData("商机", "软件", "转介绍", " "); err == nil {
		t.Fatal("blank requirement summary accepted")
	}
}

func TestVoidBlockerErrorCodeIsStable(t *testing.T) {
	wrapped := errors.Join(ErrVoidBlocked, errors.New("dependency"))
	if !errors.Is(wrapped, ErrVoidBlocked) {
		t.Fatal("void blocker must remain classifiable")
	}
}

func TestResponseIncludesLifecycleAndCompetitionFields(t *testing.T) {
	before := StatusClosed
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	model := &Opportunity{SystemCount: 3, PainPoints: "痛点", CompetitorInfo: "竞品", Status: StatusVoid, StatusBeforeVoid: &before, EndDate: &end}
	response := toResponse(model)
	if response.SystemCount != 3 || response.PainPoints != "痛点" || response.CompetitorInfo != "竞品" || response.EndDate == nil || *response.EndDate != "2026-08-01" || response.StatusBeforeVoid == nil || *response.StatusBeforeVoid != StatusClosed {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestMapExternalStatusMissingTerminalFields(t *testing.T) {
	tests := []struct{ name, status, stage, pending string }{
		{name: "win without contract", status: "投标中标", stage: StageSigned, pending: PendingContract},
		{name: "lose without reason", status: "投标落标", stage: StageFailed, pending: PendingLostReason},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stage, pending := mapExternalStatus(test.status, nil, nil)
			if stage != test.stage || pending != test.pending {
				t.Fatalf("got %s/%s want %s/%s", stage, pending, test.stage, test.pending)
			}
		})
	}
}

func TestApplyStageReopensTerminalOpportunity(t *testing.T) {
	contract := "HT001"
	model := &Opportunity{CurrentStage: StageSigned, Status: StatusClosed, ContractRef: &contract, TerminalPendingType: PendingNone}
	applyStage(model, StageRequirement, nil, nil, PendingNone, time.Now(), "user")
	if model.Status != StatusFollowing || model.ContractRef != nil || model.LostReason != nil || model.TerminalPendingType != PendingNone {
		t.Fatalf("reopen did not clear terminal fields: %#v", model)
	}
}

type contractVerifierStub struct {
	belongs bool
	err     error
}

func (s contractVerifierStub) BelongsToCustomer(context.Context, string, uint64) (bool, error) {
	return s.belongs, s.err
}

func TestLostReasonWhitelist(t *testing.T) {
	valid := "价格"
	invalid := "随便"
	if !validLostReason(&valid) {
		t.Fatal("standard reason should be accepted")
	}
	if validLostReason(&invalid) || validLostReason(nil) {
		t.Fatal("unknown or missing reason should be rejected")
	}
}

func TestGroupBoardAlwaysReturnsSevenOrderedColumns(t *testing.T) {
	items := []Response{{ID: 1, CurrentStage: StageFailed}, {ID: 2, CurrentStage: StageInitial}}
	columns := groupBoard(items)
	if len(columns) != 7 {
		t.Fatalf("got %d columns", len(columns))
	}
	if columns[0].Stage != StageInitial || len(columns[0].Items) != 1 || columns[6].Stage != StageFailed || len(columns[6].Items) != 1 {
		t.Fatalf("unexpected board: %#v", columns)
	}
	if columns[1].Items == nil {
		t.Fatal("empty columns must serialize as [] rather than null")
	}
}
