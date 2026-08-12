package presaleworker

import (
	"context"
	"crypto/tls"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/presaleworkflow"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db             *gorm.DB
	worker         *Worker
	temporalClient client.Client
	temporalWorker temporalworker.Worker
}

func New(cfg Config) (*App, error) {
	approval, pms, err := NewHTTPPorts(cfg.Approval, cfg.PMS, cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	repo := presale.NewGORMRepository(db)
	service := presale.NewService(repo, nil, nil, presale.SystemClock{}, nil)
	var temporalClient client.Client
	var temporalWorker temporalworker.Worker
	if cfg.Temporal.Enabled {
		temporalOptions := client.Options{
			HostPort:  cfg.Temporal.Address,
			Namespace: cfg.Temporal.Namespace,
		}
		if cfg.Temporal.TLS {
			temporalOptions.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			temporalOptions.ConnectionOptions.TLSDisabled = true
		}
		temporalClient, err = client.DialContext(context.Background(), temporalOptions)
		if err != nil {
			_ = dbClose(db)
			return nil, err
		}
		temporalWorker = temporalworker.New(temporalClient, cfg.Temporal.TaskQueue, temporalworker.Options{DisableRegistrationAliasing: true})
		activities := &presaleworkflow.Activities{Approval: approval, PMS: pms}
		presaleworkflow.Register(temporalWorker, activities)
		temporalPort := presaleworkflow.Client{Temporal: temporalClient, TaskQueue: cfg.Temporal.TaskQueue}
		approval = temporalPort
		pms = temporalPort
	}
	return &App{db: db, worker: NewWorker(newOutboxStore(db), service, approval, pms, cfg), temporalClient: temporalClient, temporalWorker: temporalWorker}, nil
}

func (a *App) Run(ctx context.Context) error {
	if a.temporalWorker != nil {
		if err := a.temporalWorker.Start(); err != nil {
			return err
		}
		defer a.temporalWorker.Stop()
	}
	return a.worker.Run(ctx)
}

func (a *App) Close() error {
	if a.temporalClient != nil {
		a.temporalClient.Close()
	}
	if a.db == nil {
		return nil
	}
	return dbClose(a.db)
}

func dbClose(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
