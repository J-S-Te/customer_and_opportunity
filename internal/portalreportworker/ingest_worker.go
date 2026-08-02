package portalreportworker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

type ingestStore interface {
	claim(context.Context, string, time.Time, time.Duration, int) ([]report.IngestJob, error)
	failed(context.Context, report.IngestJob, string, time.Time, string) error
}

type ingestProjection interface {
	CompleteIngest(context.Context, report.IngestJob, report.FileDescriptor, report.IngestResult) error
	MarkIngestDeadLetter(context.Context, report.IngestJob) error
}

type IngestWorker struct {
	store         ingestStore
	service       ingestProjection
	protector     report.DescriptorProtector
	ingestor      report.FileIngestor
	workerID      string
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

func newIngestWorker(store ingestStore, service ingestProjection, protector report.DescriptorProtector, ingestor report.FileIngestor, cfg Config) *IngestWorker {
	return &IngestWorker{store: store, service: service, protector: protector, ingestor: ingestor, workerID: cfg.WorkerID, leaseDuration: cfg.LeaseDuration, batchSize: cfg.BatchSize, now: func() time.Time { return time.Now().UTC() }}
}

func (w *IngestWorker) RunOnce(ctx context.Context) (int, error) {
	jobs, err := w.store.claim(ctx, w.workerID, w.now(), w.leaseDuration, w.batchSize)
	if err != nil {
		return 0, err
	}
	var joined error
	for i := range jobs {
		if err = w.dispatch(ctx, jobs[i]); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return len(jobs), joined
}

func (w *IngestWorker) dispatch(ctx context.Context, job report.IngestJob) error {
	if job.ID == 0 || job.RequestID == 0 || strings.TrimSpace(job.EventID) == "" || strings.TrimSpace(job.TenantID) == "" || len(job.DescriptorCipher) == 0 || strings.TrimSpace(job.DescriptorHash) == "" {
		return w.fail(ctx, job, errors.New("invalid Portal report ingest job"))
	}
	raw, err := w.protector.Decrypt(ctx, job.DescriptorCipher)
	if err != nil {
		return w.fail(ctx, job, err)
	}
	var descriptor report.FileDescriptor
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&descriptor); err != nil {
		return w.fail(ctx, job, errors.New("Portal report ingest descriptor is invalid"))
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return w.fail(ctx, job, errors.New("Portal report ingest descriptor is invalid"))
	}
	if reportDescriptorHash(descriptor) != job.DescriptorHash {
		return w.fail(ctx, job, errors.New("Portal report ingest descriptor binding failed"))
	}
	// Bound the provider call inside the database lease. Implementations must
	// honor cancellation; a stable eventID makes a lease replay idempotent.
	providerTimeout := w.leaseDuration - time.Second
	providerCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	result, err := w.ingestor.Ingest(providerCtx, job.EventID, descriptor)
	if err == nil {
		err = w.service.CompleteIngest(ctx, job, descriptor, result)
	}
	if err != nil {
		return w.fail(ctx, job, err)
	}
	return nil
}

func (w *IngestWorker) fail(ctx context.Context, job report.IngestJob, cause error) error {
	safe := safeLogError(cause)
	if job.RetryCount+1 > uint8(len(retryDelays)) {
		if err := w.service.MarkIngestDeadLetter(ctx, job); err != nil {
			return errors.Join(errors.New(safe), err)
		}
		return errors.New(safe)
	}
	if err := w.store.failed(ctx, job, w.workerID, w.now(), safe); err != nil {
		return errors.Join(errors.New(safe), err)
	}
	return errors.New(safe)
}

func reportDescriptorHash(value report.FileDescriptor) string {
	// Exported comparison remains intentionally unavailable; JSON re-encoding is
	// canonical for this fixed struct and mirrors the module hash calculation.
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (w *IngestWorker) Run(ctx context.Context, pollInterval time.Duration) error {
	if w.store == nil || w.service == nil || w.protector == nil || w.ingestor == nil {
		return errors.New("Portal report ingest worker dependencies are incomplete")
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal report ingest worker poll failed: %v", safeLogError(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
