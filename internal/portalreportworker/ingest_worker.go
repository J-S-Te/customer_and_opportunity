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
	// 严格 JSON 解码并校验密文所绑定的结构摘要，避免未知字段或替换后的描述符驱动受信文件服务。
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
	// 这里按“完整租约时长减一秒”设置相对调用预算，并不重新读取 locked_until 计算真实剩余租约；
	// 适配器仍须响应取消，稳定 EventID 则用于让超时或租约接管后的重复摄取保持幂等。
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
	// 固定结构重新编码后的 JSON 是模块内部约定的规范形式，与任务创建端使用同一摘要算法。
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
