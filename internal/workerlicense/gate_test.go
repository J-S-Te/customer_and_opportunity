package workerlicense

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"testing"
)

func TestTaskRechecksAndPreservesHistoryAndTechnicalDelivery(t *testing.T) {
	active := true
	ctx := WithCheck(context.Background(), func(_ context.Context, op core.Operation) error {
		if !active && op == core.MUTATE_BUSINESS {
			return errors.New("expired")
		}
		return nil
	})
	if !Allowed(ctx, core.MUTATE_BUSINESS) {
		t.Fatal("active denied")
	}
	active = false
	if !errors.Is(Require(ctx, core.MUTATE_BUSINESS), ErrDenied) {
		t.Fatal("stale task check")
	}
	for _, op := range []core.Operation{core.READ_HISTORY, core.EXPORT_HISTORY, core.ESSENTIAL_SERVICE} {
		if !Allowed(ctx, op) {
			t.Fatal("historical/safety task blocked")
		}
	}
}
