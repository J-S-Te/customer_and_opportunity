package credit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
