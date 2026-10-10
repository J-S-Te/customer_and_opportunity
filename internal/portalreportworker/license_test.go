package portalreportworker

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"testing"
)

func TestDeniedPortalBusinessTasksRemainPending(t *testing.T) {
	ctx := workerlicense.WithCheck(context.Background(), func(context.Context, core.Operation) error { return errors.New("expired") })
	w := &Worker{}
	if n, err := w.RunOnce(ctx); n != 0 || err != nil {
		t.Fatal("denied report poll did not defer")
	}
	if err := w.dispatch(ctx, report.Outbox{}); !errors.Is(err, workerlicense.ErrDenied) {
		t.Fatal("report consumed")
	}
	ingest := &IngestWorker{}
	if n, err := ingest.RunOnce(ctx); n != 0 || err != nil {
		t.Fatal("ingest not deferred")
	}
	if err := ingest.dispatch(ctx, report.IngestJob{}); !errors.Is(err, workerlicense.ErrDenied) {
		t.Fatal("ingest consumed")
	}
}
