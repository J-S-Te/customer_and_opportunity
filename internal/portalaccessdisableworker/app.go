package portalaccessdisableworker

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
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
	service := portalinvite.NewAccessDisableService(repo, nil, platformClient, portalClient, audit.NewGORMWriter(db), portalinvite.SystemClock{}, portalinvite.CryptoRandom{}, options...)
	return &App{db: db, worker: NewWorker(newStore(db), service, cfg)}, nil
}

func (a *App) Run(ctx context.Context) error { return a.worker.Run(ctx) }

func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
