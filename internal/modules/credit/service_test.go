package credit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestApplicationJSONUsesStableSnakeCaseAPIFields(t *testing.T) {
	payload, err := json.Marshal(Application{ID: 7, CustomerID: 11, ApplicantID: "sales-1", FromLevel: LevelC, TargetLevel: LevelB, CreatedAt: time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, field := range []string{`"id":7`, `"customer_id":11`, `"applicant_id":"sales-1"`, `"from_level":"C"`, `"target_level":"B"`, `"created_at"`, `"version":1`} {
		if !strings.Contains(got, field) {
			t.Fatalf("JSON missing %s: %s", field, got)
		}
	}
	for _, field := range []string{`"CustomerID"`, `"CreatedAt"`, `"ApplicantID"`} {
		if strings.Contains(got, field) {
			t.Fatalf("JSON leaked Go field %s: %s", field, got)
		}
	}
}

func TestCreditLogJSONUsesStableHistoryFields(t *testing.T) {
	payload, err := json.Marshal(CreditLog{ID: 7, CustomerID: 11, FromLevel: LevelC, ToLevel: LevelB, Source: "MANUAL", OperatorID: "sales-1", OccurredAt: time.Date(2026, 9, 1, 7, 40, 11, 421000000, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, field := range []string{`"from_level":"C"`, `"to_level":"B"`, `"operator_id":"sales-1"`, `"occurred_at"`} {
		if !strings.Contains(got, field) {
			t.Fatalf("JSON missing %s: %s", field, got)
		}
	}
	for _, field := range []string{`"FromLevel"`, `"ToLevel"`, `"OperatorID"`, `"TenantID"`} {
		if strings.Contains(got, field) {
			t.Fatalf("JSON leaked Go field %s: %s", field, got)
		}
	}
}

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
	early := valid
	earlyPaid := dueDate.Add(-time.Hour)
	early.PaidDate = &earlyPaid
	if err := validatePaymentEvent(early, due, paid); err != nil {
		t.Fatalf("early payment must be valid: %v", err)
	}
	for name, event := range map[string]PaymentEvent{
		"missing source":   func() PaymentEvent { v := valid; v.SourceSystem = ""; return v }(),
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

func TestPaymentEventReversalRequiresOriginalAndZeroPaidAmount(t *testing.T) {
	dueDate := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	paidDate := dueDate
	valid := PaymentEvent{EventID: "rev-1", EventType: PaymentEventReversed, PaymentID: "rev-pay-1", ReversalOfPaymentID: "pay-1", CustomerID: 1, DueDate: dueDate, PaidDate: &paidDate, DueAmount: "100.00", PaidAmount: "0.00", SourceSystem: "settlement"}
	due, _ := decimal.NewFromString(valid.DueAmount)
	paid, _ := decimal.NewFromString(valid.PaidAmount)
	if err := validatePaymentEvent(valid, due, paid); err != nil {
		t.Fatalf("valid reversal rejected: %v", err)
	}
	missing := valid
	missing.ReversalOfPaymentID = ""
	if err := validatePaymentEvent(missing, due, paid); err == nil {
		t.Fatal("reversal without reversal_of_payment_id must be rejected")
	}
	nonzero := valid
	nonzero.PaidAmount = "1.00"
	nonzeroPaid, _ := decimal.NewFromString(nonzero.PaidAmount)
	if err := validatePaymentEvent(nonzero, due, nonzeroPaid); err == nil {
		t.Fatal("reversal with nonzero paid_amount must be rejected")
	}
}

func TestCreditReversalMigrationStoresPreAndPostState(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000116_credit_payment_reversal.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"prev_level", "prev_ontime_count", "prev_late_count", "result_level", "result_ontime_count", "result_late_count", "reversed_at", "reversal_event_id"} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("000116 migration missing %s", required)
		}
	}
}

func TestSettlementShapedCreditPayloadBindsAndValidates(t *testing.T) {
	raw := []byte(`{"event_id":"credit-1","event_type":"customer.credit.payment.completed.v1","tenant_id":"tenant-a","payment_id":"allocation-1","customer_id":42,"contract_no":"HT-1","period_no":"1","due_date":"2099-12-31T00:00:00Z","paid_date":"2026-10-18T00:00:00Z","due_amount":"100.00","paid_amount":"100.00","source_system":"settlement"}`)
	var event PaymentEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	due, _ := decimal.NewFromString(event.DueAmount)
	paid, _ := decimal.NewFromString(event.PaidAmount)
	if err := validatePaymentEvent(event, due, paid); err != nil {
		t.Fatalf("settlement payload rejected: %v", err)
	}
	if event.CustomerID != 42 || event.PeriodNo != "1" || event.DueDate.IsZero() || event.PaidDate == nil {
		t.Fatalf("settlement payload normalized incorrectly: %+v", event)
	}
}
