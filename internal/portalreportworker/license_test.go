package portalreportworker

import (
	"context"
	"encoding/json"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"testing"
	"time"
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

func TestExpiryDuringFileIngestDefersReportIssuanceWithoutFailureProjection(t *testing.T) {
	protector, err := report.NewAESDescriptorProtector(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	descriptor := report.FileDescriptor{ObjectRef: "file", FileName: "report.pdf", MIME: "application/pdf", FileHash: "fixture-hash", Size: 1}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := protector.Encrypt(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	job := report.IngestJob{ID: 1, TenantID: "tenant-fixture", RequestID: 2, EventID: "event-fixture", DescriptorHash: reportDescriptorHash(descriptor), DescriptorCipher: encrypted, LockedBy: "worker"}
	store := &ingestStoreStub{}
	projection := &ingestProjectionStub{}
	allowed := true
	ctx := workerlicense.WithCheck(context.Background(), func(context.Context, core.Operation) error {
		if !allowed {
			return errors.New("expired")
		}
		return nil
	})
	worker := &IngestWorker{store: store, service: projection, protector: protector, ingestor: expiryIngestor{expire: func() { allowed = false }}, leaseDuration: 30 * time.Second, now: time.Now}
	if err := worker.dispatch(ctx, job); !errors.Is(err, workerlicense.ErrDenied) || projection.job.ID != 0 || store.failedJob != nil || projection.deadLetter != nil {
		t.Fatal("expired ingest issued or failed report", err)
	}
}

type expiryIngestor struct{ expire func() }

func (i expiryIngestor) Ingest(context.Context, string, report.FileDescriptor) (report.IngestResult, error) {
	i.expire()
	return report.IngestResult{}, nil
}
