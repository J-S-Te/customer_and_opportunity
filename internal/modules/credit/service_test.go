package credit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestAutomaticRuleThresholdMovesOneLevelAndCaps(t *testing.T) {
	if got := stepLevel("B", -1); got != LevelA {
		t.Fatalf("two on-time payments should promote B to A, got %q", got)
	}
	if got := stepLevel("C", 1); got != LevelD {
		t.Fatalf("two late payments should demote C to D, got %q", got)
	}
	if got := stepLevel(LevelA, -1); got != LevelA {
		t.Fatalf("A must be capped, got %q", got)
	}
	if got := stepLevel(LevelD, 1); got != LevelD {
		t.Fatalf("D must be capped, got %q", got)
	}
}

func TestAutomaticRulePaymentEventsHaveTenantScopedIdempotency(t *testing.T) {
	migrationPath := filepath.Join("..", "..", "..", "migrations", "000106_complete_customer_credit_workflow.up.sql")
	contents, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "UNIQUE KEY uq_credit_payment_id (tenant_id,payment_id)") {
		t.Fatal("payment events must be deduplicated by tenant and payment_id")
	}
}

func TestRuleSettingsRemainMonolithConfiguration(t *testing.T) {
	if !validRuleSettings(RuleSettings{GraceDays: 7, OnTimeThreshold: 2, LateThreshold: 2, LevelStep: 1, Enabled: true}) {
		t.Fatal("default credit rule settings must be valid")
	}
	for _, invalid := range []RuleSettings{{GraceDays: -1, OnTimeThreshold: 2, LateThreshold: 2, LevelStep: 1}, {GraceDays: 7, OnTimeThreshold: 0, LateThreshold: 2, LevelStep: 1}, {GraceDays: 7, OnTimeThreshold: 2, LateThreshold: 2, LevelStep: 0}} {
		if validRuleSettings(invalid) {
			t.Fatalf("invalid settings accepted: %#v", invalid)
		}
	}
}

func TestCreditApplicationRequiresDurableIdempotencyKey(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000106_complete_customer_credit_workflow.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "UNIQUE KEY uq_credit_apply_idempotency (tenant_id,applicant_id,idempotency_key)") {
		t.Fatal("credit applications must have a durable tenant/applicant idempotency constraint")
	}
}

func TestPaymentEventValidationRejectsUnsafeOrInconsistentFacts(t *testing.T) {
	dueDate := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	valid := PaymentEvent{EventID: "evt-1", PaymentID: "pay-1", CustomerID: 1, DueDate: dueDate, PaidDate: func() *time.Time { v := dueDate; return &v }(), DueAmount: "100.00", PaidAmount: "100.00", SourceSystem: "settlement"}
	due, _ := decimal.NewFromString(valid.DueAmount)
	paid, _ := decimal.NewFromString(valid.PaidAmount)
	if err := validatePaymentEvent(valid, due, paid); err != nil {
		t.Fatalf("valid payment rejected: %v", err)
	}
	for name, event := range map[string]PaymentEvent{
		"missing source":   func() PaymentEvent { v := valid; v.SourceSystem = ""; return v }(),
		"early paid date":  func() PaymentEvent { v := valid; d := dueDate.Add(-time.Hour); v.PaidDate = &d; return v }(),
		"excess precision": func() PaymentEvent { v := valid; v.PaidAmount = "1.001"; return v }(),
		"invalid contract": func() PaymentEvent { v := valid; v.ContractNo = "bad value"; return v }(),
	} {
		amountDue, _ := decimal.NewFromString(event.DueAmount)
		amountPaid, _ := decimal.NewFromString(event.PaidAmount)
		if err := validatePaymentEvent(event, amountDue, amountPaid); err == nil {
			t.Errorf("%s: invalid payment accepted", name)
		}
	}
}

func TestPaymentIdempotencyComparesFullPayload(t *testing.T) {
	dueDate := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	event := PaymentEvent{EventID: "evt-1", PaymentID: "pay-1", CustomerID: 1, DueDate: dueDate, DueAmount: "100.00", PaidAmount: "100.00", SourceSystem: "settlement"}
	record := creditPaymentRecord{EventID: event.EventID, PaymentID: event.PaymentID, CustomerID: event.CustomerID, DueDate: &dueDate, DueAmount: event.DueAmount, PaidAmount: event.PaidAmount, SourceSystem: event.SourceSystem}
	if !samePaymentPayload(record, event) {
		t.Fatal("identical payment payload must be replayable")
	}
	event.PaidAmount = "101.00"
	if samePaymentPayload(record, event) {
		t.Fatal("changed payment amount must produce an idempotency conflict")
	}
}
