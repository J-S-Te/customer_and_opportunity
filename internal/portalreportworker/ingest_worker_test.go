package portalreportworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

type ingestStoreStub struct {
	jobs      []report.IngestJob
	failedJob *report.IngestJob
}

func (s *ingestStoreStub) claim(context.Context, string, time.Time, time.Duration, int) ([]report.IngestJob, error) {
	return s.jobs, nil
}
func (s *ingestStoreStub) failed(_ context.Context, job report.IngestJob, _ string, _ time.Time, _ string) error {
	s.failedJob = &job
	return nil
}

type ingestProjectionStub struct {
	job        report.IngestJob
	descriptor report.FileDescriptor
	result     report.IngestResult
	deadLetter *report.IngestJob
}

func (s *ingestProjectionStub) MarkIngestDeadLetter(_ context.Context, job report.IngestJob) error {
	s.deadLetter = &job
	return nil
}

func (s *ingestProjectionStub) CompleteIngest(_ context.Context, job report.IngestJob, descriptor report.FileDescriptor, result report.IngestResult) error {
	s.job, s.descriptor, s.result = job, descriptor, result
	return nil
}

type ingestProviderStub struct {
	eventID string
	input   report.FileDescriptor
	err     error
}

func (s *ingestProviderStub) Ingest(_ context.Context, eventID string, input report.FileDescriptor) (report.IngestResult, error) {
	s.eventID, s.input = eventID, input
	return report.IngestResult{ScanStatus: "CLEAN"}, s.err
}

func TestIngestWorkerBindsStableEventAndDescriptor(t *testing.T) {
	protector, err := report.NewAESDescriptorProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor := report.FileDescriptor{ObjectRef: "trusted/report.pdf", FileName: "report.pdf", MIME: "application/pdf", FileHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 10}
	raw := []byte(`{"ObjectRef":"trusted/report.pdf","FileName":"report.pdf","MIME":"application/pdf","FileHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Size":10}`)
	ciphertext, err := protector.Encrypt(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	job := report.IngestJob{ID: 1, EventID: "stable-event", TenantID: "tenant-1", CustomerID: 2, RequestID: 3, DescriptorCipher: ciphertext, DescriptorHash: reportDescriptorHash(descriptor), Status: report.IngestProcessing, LockedBy: "worker-1"}
	store, projection, provider := &ingestStoreStub{jobs: []report.IngestJob{job}}, &ingestProjectionStub{}, &ingestProviderStub{}
	worker := &IngestWorker{store: store, service: projection, protector: protector, ingestor: provider, workerID: "worker-1", leaseDuration: 30 * time.Second, batchSize: 1, now: time.Now}
	if count, err := worker.RunOnce(context.Background()); err != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if provider.eventID != job.EventID || provider.input != descriptor || projection.job.EventID != job.EventID || projection.descriptor != descriptor || store.failedJob != nil {
		t.Fatalf("provider=%+v projection=%+v failed=%+v", provider, projection, store.failedJob)
	}
}

func TestIngestWorkerRejectsDescriptorHashMismatchBeforeProvider(t *testing.T) {
	protector, _ := report.NewAESDescriptorProtector([]byte("0123456789abcdef0123456789abcdef"))
	ciphertext, _ := protector.Encrypt(context.Background(), []byte(`{"ObjectRef":"trusted/report.pdf","FileName":"report.pdf","MIME":"application/pdf","FileHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Size":10}`))
	job := report.IngestJob{ID: 1, EventID: "stable-event", TenantID: "tenant-1", RequestID: 3, DescriptorCipher: ciphertext, DescriptorHash: "wrong", Status: report.IngestProcessing, LockedBy: "worker-1"}
	store, provider := &ingestStoreStub{jobs: []report.IngestJob{job}}, &ingestProviderStub{err: errors.New("must not be reached")}
	worker := &IngestWorker{store: store, service: &ingestProjectionStub{}, protector: protector, ingestor: provider, workerID: "worker-1", leaseDuration: 30 * time.Second, batchSize: 1, now: time.Now}
	if _, err := worker.RunOnce(context.Background()); err == nil || store.failedJob == nil || provider.eventID != "" {
		t.Fatalf("err=%v failed=%+v provider=%+v", err, store.failedJob, provider)
	}
}

func TestIngestWorkerTerminalFailureUpdatesProjection(t *testing.T) {
	protector, _ := report.NewAESDescriptorProtector([]byte("0123456789abcdef0123456789abcdef"))
	ciphertext, _ := protector.Encrypt(context.Background(), []byte(`{"ObjectRef":"trusted/report.pdf","FileName":"report.pdf","MIME":"application/pdf","FileHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Size":10}`))
	descriptor := report.FileDescriptor{ObjectRef: "trusted/report.pdf", FileName: "report.pdf", MIME: "application/pdf", FileHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 10}
	job := report.IngestJob{ID: 1, EventID: "stable-event", TenantID: "tenant-1", RequestID: 3, DescriptorCipher: ciphertext, DescriptorHash: reportDescriptorHash(descriptor), Status: report.IngestProcessing, LockedBy: "worker-1", RetryCount: 6}
	store, projection := &ingestStoreStub{jobs: []report.IngestJob{job}}, &ingestProjectionStub{}
	worker := &IngestWorker{store: store, service: projection, protector: protector, ingestor: &ingestProviderStub{err: errors.New("provider failed")}, workerID: "worker-1", leaseDuration: 30 * time.Second, batchSize: 1, now: time.Now}
	if _, err := worker.RunOnce(context.Background()); err == nil || projection.deadLetter == nil || store.failedJob != nil {
		t.Fatalf("err=%v projection=%+v store=%+v", err, projection, store)
	}
}
