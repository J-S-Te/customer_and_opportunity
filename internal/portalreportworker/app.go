package portalreportworker

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db           *gorm.DB
	worker       *Worker
	ingest       *IngestWorker
	heartbeats   *workerruntime.Repository
	workerID     string
	pollInterval time.Duration
}

func New(cfg Config) (*App, error) {
	projectClient, err := newProjectServiceClient(cfg.Project)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	repo := report.NewGORMRepository(db)
	protector, err := report.NewAESDescriptorProtector(cfg.IngestDescriptorKey)
	if err != nil {
		return nil, err
	}
	service := report.NewService(repo, nil, nil, protector, workerClock{}, nil)
	return &App{db: db, worker: NewWorker(newOutboxStore(db), service, projectClient, cfg), ingest: newIngestWorker(newIngestJobStore(db), service, protector, unavailableFileIngestor{}, cfg), heartbeats: workerruntime.NewRepository(db), workerID: cfg.WorkerID, pollInterval: cfg.PollInterval}, nil
}

func (a *App) Run(ctx context.Context) error {
	startedAt := time.Now().UTC()
	if err := a.heartbeats.Start(ctx, workerruntime.ReportDeliveryWorker, a.workerID, startedAt); err != nil {
		return err
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	var heartbeatWait sync.WaitGroup
	heartbeatWait.Add(1)
	defer func() {
		stopHeartbeat()
		heartbeatWait.Wait()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.heartbeats.Remove(cleanupCtx, workerruntime.ReportDeliveryWorker, a.workerID, startedAt); err != nil {
			log.Printf("remove Portal report worker heartbeat: %v", err)
		}
	}()
	go func() {
		defer heartbeatWait.Done()
		workerruntime.RefreshLoop(heartbeatCtx, a.heartbeats, workerruntime.ReportDeliveryWorker, a.workerID, startedAt, func(err error) {
			log.Printf("refresh Portal report worker heartbeat: %v", err)
		})
	}()
	processingCtx, stopProcessing := context.WithCancel(ctx)
	results := make(chan error, 2)
	// 审批投递和文件摄取共享实例心跳但独立轮询；任一循环退出都会取消并等待另一循环，避免残留孤儿协程。
	go func() { results <- a.worker.Run(processingCtx) }()
	go func() { results <- a.ingest.Run(processingCtx, a.pollInterval) }()
	err := <-results
	stopProcessing()
	// 删除心跳前必须等待兄弟循环退出；实例存活证据消失后，不能再以该 WorkerID 继续处理 Portal 任务。
	otherErr := <-results
	if errors.Is(err, context.Canceled) {
		return otherErr
	}
	if errors.Is(otherErr, context.Canceled) {
		return err
	}
	return errors.Join(err, otherErr)
}

func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

type workerClock struct{}

func (workerClock) Now() time.Time { return time.Now().UTC() }

type unavailableFileIngestor struct{}

func (unavailableFileIngestor) Ingest(context.Context, string, report.FileDescriptor) (report.IngestResult, error) {
	// 默认实现故意失败关闭：未配置可信对象存储、病毒扫描和加密链路时，绝不把描述符误记为已摄取。
	return report.IngestResult{}, errors.New("trusted report object-storage, scanning and encryption provider is not configured")
}
