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
	migrationPath := filepath.Join("..", "..", "..", "migrations", "000105_customer_credit.up.sql")
	contents, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "UNIQUE KEY uq_credit_payment (tenant_id, event_id)") {
		t.Fatal("payment events must be deduplicated by tenant and stable event_id")
	}
}
