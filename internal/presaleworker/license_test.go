package presaleworker

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"testing"
)

func TestDeniedBusinessTaskDoesNotClaimOrDeadLetter(t *testing.T) {
	ctx := workerlicense.WithCheck(context.Background(), func(context.Context, core.Operation) error { return errors.New("expired") })
	w := &Worker{}
	if n, err := w.RunOnce(ctx); n != 0 || err != nil {
		t.Fatal("denied poll did not defer")
	}
	if err := w.dispatch(ctx, presale.OutboxEvent{}); !errors.Is(err, workerlicense.ErrDenied) {
		t.Fatal("claimed task consumed or advanced")
	}
}
