package portalbootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
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
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	Config Config
	DB     *gorm.DB
	Server *http.Server
}

func New(ctx context.Context, config Config) (*App, error) {
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
	accountService := account.NewService(account.NewGORMRepository(db), oidcAdapter, inviteClient, protector, account.SystemClock{}, account.CryptoRandom{}, config.RoleConfigHash, config.SessionTTL)
	projectService := project.NewService(project.NewGORMRepository(db), unavailableProjectSource{})
	workerReadiness := workerruntime.NewRepository(db)
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
	router := NewRouter(RouterDependencies{Config: config, Account: accountService, Projects: projectService, ProjectExports: projectExportService, ProjectMessages: projectMessageService, Reports: reportService, ReportDownloads: reportDownloadService, WorkerReadiness: workerReadiness, WorkerHeartbeatMaxAge: workerruntime.HeartbeatMaxAge, ReportDownloadError: func(ctx context.Context, err error) {
		slog.Default().ErrorContext(ctx, "Portal report download completion audit failed", "error", err)
	}, Feedback: feedbackService, Evaluations: evaluationService, Filings: filingService, FilingMaterials: filingMaterialService, MachineAuthenticator: machineAuthenticator, DatabaseHealthy: func() bool {
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return sqlDB.PingContext(pingCtx) == nil
	}, Logger: slog.Default()})
	return &App{Config: config, DB: db, Server: &http.Server{Addr: config.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}}, nil
}

func (a *App) Close(ctx context.Context) error {
	shutdownErr := a.Server.Shutdown(ctx)
	sqlDB, err := a.DB.DB()
	if err == nil {
		err = sqlDB.Close()
	}
	return errors.Join(shutdownErr, err)
}

type projectAccess struct{ projects *project.Service }

func (a projectAccess) Accessible(ctx context.Context, tenant string, customer uint64, projectID string) (bool, error) {
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
