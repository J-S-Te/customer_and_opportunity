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
	worker.withReconciler(newReconciler(newReconciliationStore(db), portalClient, cfg.WorkerID, cfg.ReconciliationBatchSize), cfg.ReconciliationInterval)
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
