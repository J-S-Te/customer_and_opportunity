package portalreportworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/reportsecurity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db            *gorm.DB
	worker        *Worker
	ingest        *IngestWorker
	heartbeats    *workerruntime.Repository
	workerID      string
	pollInterval  time.Duration
	files         *reportsecurity.EncryptedStore
	heartbeatType string
}

// New 装配真实投递与安全摄取，显式本地模式仅允许摄取现有LOCAL来源。
// 参数：cfg为固定作业与来源配置。
// 返回值：工作器或来源、依赖错误。
func New(cfg Config) (*App, error) {
	if cfg.LocalDirectoryMode != "" && cfg.LocalDirectoryMode != "crm" && cfg.LocalDirectoryMode != "local" {
		return nil, fmt.Errorf("invalid PORTAL_CUSTOMER_DIRECTORY_MODE")
	}
	if cfg.LocalDirectoryMode == "local" && !cfg.IngestOnly {
		return nil, fmt.Errorf("local directory mode requires ingest-only report worker")
	}
	var projectClient *projectServiceClient
	var err error
	if !cfg.IngestOnly {
		projectClient, err = newProjectServiceClient(cfg.Project)
		if err != nil {
			return nil, err
		}
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
	ingestor, files, err := newFileIngestor(cfg, protector)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	heartbeatType := workerruntime.ReportIngestWorker
	var delivery *Worker
	if !cfg.IngestOnly {
		delivery = NewWorker(newOutboxStore(db, service), service, projectClient, cfg)
		heartbeatType = workerruntime.ReportDeliveryWorker
		if cfg.ProjectPreflight {
			if probeErr := runProjectPreflight(projectClient, cfg.Project.RequestURL); probeErr != nil {
				// 自检失败只关闭新申请投递准入（不发布投递心跳），摄取侧仍继续处理既有回调。
				log.Printf("Portal report delivery preflight failed; delivery heartbeat withheld: %v", safeLogError(probeErr))
				delivery, heartbeatType = nil, workerruntime.ReportIngestWorker
			}
		}
	}
	return &App{db: db, worker: delivery, ingest: newIngestWorker(newIngestJobStore(db), service, protector, ingestor, cfg), heartbeats: workerruntime.NewRepository(db), workerID: cfg.WorkerID, pollInterval: cfg.PollInterval, files: files, heartbeatType: heartbeatType}, nil
}

func (a *App) Run(ctx context.Context) error {
	startedAt := time.Now().UTC()
	if err := a.heartbeats.Start(ctx, a.heartbeatType, a.workerID, startedAt); err != nil {
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
		if err := a.heartbeats.Remove(cleanupCtx, a.heartbeatType, a.workerID, startedAt); err != nil {
			log.Printf("remove Portal report worker heartbeat: %v", err)
		}
	}()
	go func() {
		defer heartbeatWait.Done()
		workerruntime.RefreshLoop(heartbeatCtx, a.heartbeats, a.heartbeatType, a.workerID, startedAt, func(err error) {
			log.Printf("refresh Portal report worker heartbeat: %v", err)
		})
	}()
	processingCtx, stopProcessing := context.WithCancel(ctx)
	results := make(chan error, 2)
	// 审批投递和文件摄取共享实例心跳但独立轮询；任一循环退出都会取消并等待另一循环，避免残留孤儿协程。
	if a.worker != nil {
		go func() { results <- a.worker.Run(processingCtx) }()
	}
	go func() { results <- a.ingest.Run(processingCtx, a.pollInterval) }()
	err := <-results
	stopProcessing()
	// 删除心跳前必须等待兄弟循环退出；实例存活证据消失后，不能再以该 WorkerID 继续处理 Portal 任务。
	var otherErr error
	if a.worker != nil {
		otherErr = <-results
	}
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
	return errors.Join(sqlDB.Close(), a.files.Close())
}

type workerClock struct{}

func (workerClock) Now() time.Time { return time.Now().UTC() }

// runProjectPreflight 在完整投递模式启动时用一次不携带业务载荷的请求探测下游端点；
// 复用 OAuth 客户端凭据传输层（首个请求即取 token），只判断端点可达且未拒绝该机器身份。
func runProjectPreflight(client *projectServiceClient, endpoint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return preflightProjectEndpoint(ctx, client, endpoint)
}

// preflightProjectEndpoint 只做连通性与身份可用性探测：401/403（凭据或 scope 不匹配）与 5xx
// （下游不健康）视为失败；4xx 协议反馈说明端点可达，不阻断启动。
func preflightProjectEndpoint(ctx context.Context, client *projectServiceClient, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if client == nil || client.client == nil || endpoint == "" {
		return errors.New("Portal report project-service preflight is not configured")
	}
	if parsed, err := url.ParseRequestURI(endpoint); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("Portal report project-service preflight endpoint is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return errors.New("Portal report project-service preflight request is invalid")
	}
	resp, err := client.client.Do(req)
	if err != nil {
		return safeTransportError(err)
	}
	defer resp.Body.Close()
	// 探测响应正文不参与判定，只做有界丢弃，避免异常下游占用连接。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
		return fmt.Errorf("project-service preflight status=%d", resp.StatusCode)
	}
	return nil
}
