package portalbootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/capability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/evaluation"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/feedback"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/filing"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/machineauth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectexport"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectmessage"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	Config      Config
	DB          *gorm.DB
	Server      *http.Server
	auditCancel context.CancelFunc
	auditDone   chan struct{}
}

func New(ctx context.Context, config Config) (*App, error) {
	// Portal 在开放端口前真实探测数据库并构造所有信任边界；认证、密钥或机器验签材料有误时
	// 直接失败，避免部分路由在不完整安全配置下运行。
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, fmt.Errorf("open Portal database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get Portal database handle: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping Portal database: %w", err)
	}
	auditStore := requestaudit.NewPortalStore(db)
	auditDispatcher, err := requestaudit.NewDispatcher(auditStore, requestaudit.DispatcherOptions{
		PlatformBaseURL: config.PlatformBaseURL, ClientID: config.PlatformAuditClientID,
		ClientSecret: config.PlatformAuditClientSecret, ApplicationCode: config.PlatformApplicationCode,
		EnvironmentCode: config.PlatformEnvironmentCode, WorkerID: config.PlatformAuditWorkerID,
		PollInterval: config.PlatformAuditPollInterval, BatchSize: config.PlatformAuditBatchSize,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Portal request audit: %w", err)
	}
	oidcAdapter, err := NewOIDCAdapter(ctx, config)
	if err != nil {
		return nil, err
	}
	machineAuthenticator, err := machineauth.New(ctx, db, machineauth.Options{
		Issuer: config.MachineTokenIssuer, Audience: config.MachineTokenAudience,
		PublicKeyPath: config.MachineTokenPublicKeyPath, TenantID: config.TenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Portal machine authentication: %w", err)
	}
	protector, err := NewSecretProtector(config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	inviteClient, err := NewCRMInviteClient(ctx, CRMInviteClientOptions{
		BaseURL: config.CRMInviteBaseURL, TokenURL: config.CRMInviteTokenURL,
		ClientID: config.CRMInviteClientID, ClientSecret: config.CRMInviteClientSecret,
		Scope: config.CRMInviteScope,
	})
	if err != nil {
		return nil, err
	}
	var accountServiceOptions []account.ServiceOption
	if config.UsePlatformBinding {
		// Phase 4：平台绑定开启后，非邀请登录以 authorization-context 的 customer_ref 为
		// 客户边界；邀请链路与回退仍使用本地 portal_identity_links。
		accountServiceOptions = append(accountServiceOptions, account.WithPlatformBinding())
	}
	accountService := account.NewServiceWithOptions(account.NewGORMRepository(db), oidcAdapter, inviteClient, protector, account.SystemClock{}, account.CryptoRandom{}, config.RoleConfigHash, config.SessionTTL, config.PlatformEnvironmentCode, accountServiceOptions...)
	// 登录事务、访问令牌和会话均保存在数据库并加密；账号服务通过 UserInfo 周期性重验当前
	// 权限，避免仅依赖首次登录时的 ID Token 快照。
	projectService := project.NewService(project.NewGORMRepository(db), unavailableProjectSource{})
	// 尚未接入的项目源、文件读取和恶意文件扫描均使用失败关闭适配器。读模型可以继续提供
	// 已同步数据，但不能伪装实时同步或安全下载能力可用。
	workerReadiness := workerruntime.NewRepository(db)
	customerCapabilities := capability.NewCachedReader(capability.NewGORMStore(db), 30*time.Second)
	projectExportService := projectexport.NewService(projectexport.NewGORMRepository(db), projectService, systemClock{}, requestIDGenerator{}, 15*time.Minute).
		UseWorkerReadiness(workerReadiness, workerruntime.HeartbeatMaxAge)
	projectMessageService := projectmessage.NewService(projectmessage.NewGORMRepository(db), systemClock{}, requestIDGenerator{})
	codec, err := NewAEADCodec(config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	reportDescriptorProtector, err := report.NewAESDescriptorProtector(config.ReportIngestDescriptorKey)
	if err != nil {
		return nil, err
	}
	reportService := report.NewService(report.NewGORMRepository(db), projectAccess{projects: projectService}, emailProtector{codec: codec}, reportDescriptorProtector, systemClock{}, requestIDGenerator{}).
		UseWorkerReadiness(workerReadiness, workerruntime.HeartbeatMaxAge)
	reportDownloadService := report.NewDownloadService(report.NewGORMRepository(db), unavailableReportFileReader{}, nil, systemClock{}, requestIDGenerator{}, report.CryptoTokenGenerator{}, 72*time.Hour).RequireProductionSecurity(nil)
	feedbackService := feedback.NewService(feedback.NewGORMRepository(db), projectAccess{projects: projectService}, contactProtector{codec: codec}, systemClock{}, requestIDGenerator{})
	evaluationService := evaluation.NewService(evaluation.NewGORMRepository(db), projectAccess{projects: projectService}, systemClock{}, requestIDGenerator{})
	filingRepository := filing.NewGORMRepository(db)
	filingService := filing.NewService(filingRepository, filingProtector{codec: codec}, projectAccess{projects: projectService}, systemClock{}, requestIDGenerator{})
	filingMaterialService := filing.NewMaterialService(filingRepository, filingProtector{codec: codec}, filing.UnavailableMaterialObjectStore{}, filing.UnavailableMaterialScanner{}, systemClock{}, requestIDGenerator{})
	router := NewRouter(RouterDependencies{Config: config, RequestAudit: middleware.RequestAudit(auditStore, middleware.RequestAuditOptions{
		TenantID: config.TenantID, ApplicationCode: config.PlatformApplicationCode, EnvironmentCode: config.PlatformEnvironmentCode,
	}), Account: accountService, Projects: projectService, ProjectExports: projectExportService, ProjectMessages: projectMessageService, Reports: reportService, ReportDownloads: reportDownloadService, WorkerReadiness: workerReadiness, WorkerHeartbeatMaxAge: workerruntime.HeartbeatMaxAge, ReportDownloadError: func(ctx context.Context, err error) {
		slog.Default().ErrorContext(ctx, "Portal report download completion audit failed", "error", err)
	}, Feedback: feedbackService, Evaluations: evaluationService, Filings: filingService, FilingMaterials: filingMaterialService, MachineAuthenticator: machineAuthenticator, CustomerCapabilities: customerCapabilities, DatabaseHealthy: func() bool {
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return sqlDB.PingContext(pingCtx) == nil
	}, Logger: slog.Default()})
	auditContext, auditCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		auditDispatcher.Run(auditContext)
	}()
	return &App{Config: config, DB: db, Server: &http.Server{Addr: config.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}, auditCancel: auditCancel, auditDone: auditDone}, nil
}

func (a *App) Close(ctx context.Context) error {
	// HTTP 停机与连接池关闭的错误都要保留，便于编排器区分在途请求超时和数据库释放失败。
	shutdownErr := a.Server.Shutdown(ctx)
	if a.auditCancel != nil {
		a.auditCancel()
	}
	if a.auditDone != nil {
		select {
		case <-a.auditDone:
		case <-ctx.Done():
			shutdownErr = errors.Join(shutdownErr, ctx.Err())
		}
	}
	sqlDB, err := a.DB.DB()
	if err == nil {
		err = sqlDB.Close()
	}
	return errors.Join(shutdownErr, err)
}

type projectAccess struct{ projects *project.Service }

func (a projectAccess) Accessible(ctx context.Context, tenant string, customer uint64, projectID string) (bool, error) {
	// 项目可见性必须通过租户、客户和项目三元组查询；仅持有 projectID 不构成客户访问权。
	_, err := a.projects.Get(ctx, project.Scope{TenantID: tenant, CustomerID: customer}, projectID)
	if errors.Is(err, project.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (a projectAccess) Status(ctx context.Context, tenant string, customer uint64, projectID string) (string, bool, error) {
	value, err := a.projects.StatusForEvaluation(ctx, project.Scope{TenantID: tenant, CustomerID: customer}, projectID)
	if errors.Is(err, project.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

type emailProtector struct{ codec *AEADCodec }

func (p emailProtector) Encrypt(_ context.Context, value string) ([]byte, error) {
	return p.codec.Encrypt([]byte(strings.TrimSpace(value)))
}

type contactProtector struct{ codec *AEADCodec }

func (p contactProtector) Encrypt(_ context.Context, value string) ([]byte, string, error) {
	// 密文用于服务端处理，掩码仅供界面展示；空值也加密，从存储形态上不泄露“是否填写”。
	value = strings.TrimSpace(value)
	cipher, err := p.codec.Encrypt([]byte(value))
	if err != nil {
		return nil, "", err
	}
	if value == "" {
		return cipher, "", nil
	}
	runes := []rune(value)
	if len(runes) <= 4 {
		return cipher, strings.Repeat("*", len(runes)), nil
	}
	return cipher, string(runes[:2]) + strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-2:]), nil
}

type filingProtector struct{ codec *AEADCodec }

func (p filingProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) {
	return p.codec.Encrypt(value)
}

func (p filingProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) {
	return p.codec.Decrypt(value)
}

type unavailableReportFileReader struct{}

func (unavailableReportFileReader) Available() bool { return false }

func (unavailableReportFileReader) OpenVerified(context.Context, *report.File) (report.PreparedDownload, error) {
	return report.PreparedDownload{}, report.ErrDownloadUnavailable
}

type unavailableProjectSource struct{}

func (unavailableProjectSource) ChangedProjects(context.Context, string, uint64, string) ([]project.Bundle, error) {
	return nil, errors.New("project snapshot source adapter is not configured")
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type requestIDGenerator struct{}

func (requestIDGenerator) NewID() string { return request.NewID() }
