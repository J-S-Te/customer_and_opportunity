package portalaccessdisableworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db          *gorm.DB
	worker      *Worker
	auditCancel context.CancelFunc
	auditDone   chan struct{}
}

func New(ctx context.Context, cfg Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	var portalClient portalinvite.PortalMappingDisabler
	if !cfg.PlatformOnly {
		client, portalErr := portalinvite.NewHTTPPortalMappingDisabler(ctx, portalinvite.PortalMappingDisablerOptions{
			Endpoint: cfg.Portal.DisableURL, TokenURL: cfg.Portal.TokenURL, ClientID: cfg.Portal.ClientID,
			ClientSecret: cfg.Portal.ClientSecret, Scope: cfg.Portal.Scope, TLS: cfg.Portal.TLS,
		})
		if portalErr != nil {
			_ = sqlDB.Close()
			return nil, portalErr
		}
		portalClient = client
	}
	platformClient, err := portalinvite.NewHTTPPlatformRoleRevoker(ctx, portalinvite.PlatformRoleRevokerOptions{
		Endpoint: cfg.Platform.RoleRevokeURL, TokenURL: cfg.Platform.TokenURL, ClientID: cfg.Platform.ClientID,
		ClientSecret: cfg.Platform.ClientSecret, Scope: cfg.Platform.Scope, ApplicationCode: cfg.Platform.ApplicationCode, TLS: cfg.Platform.TLS,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	repo := portalinvite.NewGORMRepository(db)
	var options []portalinvite.AccessDisableServiceOption
	if cfg.PlatformOnly {
		bindingDisabler, disableErr := portalinvite.NewHTTPPlatformBindingDisabler(ctx, portalinvite.PlatformBindingDisablerOptions{
			BaseURL: cfg.Binding.BaseURL, TokenURL: cfg.Platform.TokenURL,
			ClientID: cfg.Binding.ClientID, ClientSecret: cfg.Binding.ClientSecret,
			Scope: cfg.Binding.Scope, ApplicationCode: cfg.Platform.ApplicationCode, TLS: cfg.Binding.TLS,
		})
		if disableErr != nil {
			_ = sqlDB.Close()
			return nil, disableErr
		}
		options = append(options, portalinvite.WithPlatformBindingDisabler(bindingDisabler), portalinvite.WithPlatformOnlyDisable())
	}
	auditStore := requestaudit.NewStore(db)
	auditDispatcher, err := newAuditDispatcher(auditStore, cfg)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("initialize Portal access disable audit: %w", err)
	}
	// Write keeps the existing local crm_audit_events fact and, in the same
	// database transaction when one exists, appends a durable platform-delivery
	// record. A platform outage therefore cannot undo access disabling.
	auditWriter := audit.NewGORMWriter(db).UsePlatformOutbox(auditStore, cfg.Audit.EnvironmentCode)
	service := portalinvite.NewAccessDisableService(repo, nil, platformClient, portalClient, auditWriter, portalinvite.SystemClock{}, portalinvite.CryptoRandom{}, options...)
	auditContext, auditCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		auditDispatcher.Run(auditContext)
	}()
	go func() {
		preflightCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := auditDispatcher.ValidateConfiguration(preflightCtx); err != nil {
			slog.Default().Warn("Portal access disable audit publisher preflight failed", "error_code", requestaudit.DeliveryErrorCode(err))
		}
	}()
	return &App{db: db, worker: NewWorker(newStore(db), service, cfg), auditCancel: auditCancel, auditDone: auditDone}, nil
}

func newAuditDispatcher(store *requestaudit.Store, cfg Config) (*requestaudit.Dispatcher, error) {
	return requestaudit.NewDispatcher(store, requestaudit.DispatcherOptions{
		PlatformBaseURL: cfg.Audit.BaseURL, ClientID: cfg.Audit.ClientID, ClientSecret: cfg.Audit.ClientSecret,
		ApplicationCode: cfg.Audit.ApplicationCode, EnvironmentCode: cfg.Audit.EnvironmentCode, WorkerID: cfg.Audit.WorkerID,
		PollInterval: cfg.Audit.PollInterval, BatchSize: cfg.Audit.BatchSize,
	})
}

func (a *App) Run(ctx context.Context) error { return a.worker.Run(ctx) }

func (a *App) Close() error {
	if a.auditCancel != nil {
		a.auditCancel()
	}
	var shutdownErr error
	if a.auditDone != nil {
		select {
		case <-a.auditDone:
		case <-time.After(5 * time.Second):
			shutdownErr = errors.New("Portal access disable audit dispatcher did not stop within 5s")
		}
	}
	sqlDB, err := a.db.DB()
	if err != nil {
		return errors.Join(shutdownErr, err)
	}
	return errors.Join(shutdownErr, sqlDB.Close())
}
