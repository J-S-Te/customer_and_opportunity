package projectexportworker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db         *gorm.DB
	worker     *Worker
	heartbeats *workerruntime.Repository
	workerID   string
}

func New(ctx context.Context, cfg Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, fmt.Errorf("open project export database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping project export database: %w", err)
	}
	renderer, err := NewPDFCPURenderer(cfg.PDFConfigRoot, cfg.CJKTFontPath)
	if err != nil {
		return nil, fmt.Errorf("initialize project PDF renderer: %w", err)
	}
	return &App{db: db, worker: NewWorker(NewGORMStore(db), renderer, cfg.WorkerID, cfg.PollInterval, cfg.LeaseDuration), heartbeats: workerruntime.NewRepository(db), workerID: cfg.WorkerID}, nil
}
func (a *App) Run(ctx context.Context) error {
	startedAt := time.Now().UTC()
	if err := a.heartbeats.Start(ctx, workerruntime.ProjectExportWorker, a.workerID, startedAt); err != nil {
		return err
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	var heartbeatWait sync.WaitGroup
	heartbeatWait.Add(1)
	defer func() {
		// 先停止并等待刷新协程，再删除带 startedAt 的本次实例心跳，避免旧实例误删重启后同 ID 的新心跳。
		stopHeartbeat()
		heartbeatWait.Wait()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.heartbeats.Remove(cleanupCtx, workerruntime.ProjectExportWorker, a.workerID, startedAt); err != nil {
			log.Printf("remove Portal project export worker heartbeat: %v", err)
		}
	}()
	go func() {
		defer heartbeatWait.Done()
		workerruntime.RefreshLoop(heartbeatCtx, a.heartbeats, workerruntime.ProjectExportWorker, a.workerID, startedAt, func(err error) {
			log.Printf("refresh Portal project export worker heartbeat: %v", err)
		})
	}()
	// 渲染循环退出后由 defer 收敛心跳生命周期，监控不会看到已停止 Worker 继续存活。
	return a.worker.Run(ctx)
}
func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
