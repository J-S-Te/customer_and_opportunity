package portalinvitecompensationworker

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db     *gorm.DB
	worker *Worker
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
	portalClient, err := portalinvite.NewHTTPPortalProvisioner(ctx, portalinvite.PortalProvisionerOptions{
		Endpoint: cfg.Portal.ProvisionURL, TokenURL: cfg.Portal.TokenURL,
		ClientID: cfg.Portal.ClientID, ClientSecret: cfg.Portal.ClientSecret,
		Scope: cfg.Portal.Scope, TLS: cfg.Portal.TLS,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	roleClient, err := portalinvite.NewHTTPPlatformRoleAssigner(ctx, portalinvite.PlatformRoleAssignerOptions{
		Endpoint: cfg.Platform.RoleAssignURL, TokenURL: cfg.Platform.TokenURL,
		ClientID: cfg.Platform.ClientID, ClientSecret: cfg.Platform.ClientSecret,
		Scope: cfg.Platform.Scope, ApplicationCode: cfg.Platform.ApplicationCode,
		TLS: cfg.Platform.TLS,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	worker := NewWorker(newStore(db), httpRoleAssigner{client: roleClient}, httpMappingProvisioner{client: portalClient}, cfg)
	var platformObserver httpBindingStatusReader
	if cfg.Binding.Enabled {
		bindingWriter, bindErr := portalinvite.NewHTTPPlatformBindingWriter(ctx, portalinvite.PlatformBindingWriterOptions{
			BaseURL: cfg.Binding.BaseURL, TokenURL: cfg.Binding.TokenURL,
			ClientID: cfg.Binding.ClientID, ClientSecret: cfg.Binding.ClientSecret,
			Scope: cfg.Binding.Scope, ApplicationCode: cfg.Binding.ApplicationCode, TLS: cfg.Binding.TLS,
		})
		if bindErr != nil {
			_ = sqlDB.Close()
			return nil, bindErr
		}
		repair := httpBindingRepair{writer: bindingWriter}
		platformObserver = httpBindingStatusReader{writer: bindingWriter}
		if cfg.Binding.DisableClientID != "" {
			bindingDisabler, disableErr := portalinvite.NewHTTPPlatformBindingDisabler(ctx, portalinvite.PlatformBindingDisablerOptions{
				BaseURL: cfg.Binding.BaseURL, TokenURL: cfg.Binding.TokenURL,
				ClientID: cfg.Binding.DisableClientID, ClientSecret: cfg.Binding.DisableClientSecret,
				Scope: cfg.Binding.DisableScope, ApplicationCode: cfg.Binding.ApplicationCode, TLS: cfg.Binding.TLS,
			})
			if disableErr != nil {
				_ = sqlDB.Close()
				return nil, disableErr
			}
			repair.disabler = bindingDisabler
		}
		worker.withBindingRepair(repair)
	}
	reconciler := newReconciler(newReconciliationStore(db), portalClient, cfg.WorkerID, cfg.ReconciliationBatchSize)
	if cfg.Binding.Enabled {
		reconciler.withPlatform(platformObserver)
	}
	worker.withReconciler(reconciler, cfg.ReconciliationInterval)
	return &App{db: db, worker: worker}, nil
}

func (a *App) Run(ctx context.Context) error { return a.worker.Run(ctx) }

func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
