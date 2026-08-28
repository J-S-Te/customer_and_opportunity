package presaleworker

import (
	"context"
	"crypto/tls"
	"log"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/presaleworkflow"
	workerruntime "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/temporalworker"
	"go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db             *gorm.DB
	worker         *Worker
	temporalClient client.Client
	temporalWorker sdkworker.Worker
	metrics        *workerruntime.MetricsRegistry
	temporalConfig TemporalConfig
}

func New(cfg Config) (*App, error) {
	var approval presale.ApprovalCommandPort
	var pms presale.PMSPublisher
	var err error
	if cfg.Temporal.Internal {
		ports := internalPorts{}
		approval, pms = ports, ports
	} else {
		approval, pms, err = NewHTTPPorts(cfg.Approval, cfg.PMS, cfg.AllowInsecureHTTP)
		if err != nil {
			return nil, err
		}
	}
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	repo := presale.NewGORMRepository(db)
	service := presale.NewService(repo, nil, nil, presale.SystemClock{}, nil)
	var temporalClient client.Client
	var temporalWorker sdkworker.Worker
	var metrics *workerruntime.MetricsRegistry
	if cfg.Temporal.Enabled {
		metrics = workerruntime.NewMetricsRegistry()
		temporalOptions := client.Options{
			HostPort:       cfg.Temporal.Address,
			Namespace:      cfg.Temporal.Namespace,
			MetricsHandler: metrics,
		}
		if cfg.Temporal.TLS {
			temporalOptions.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			temporalOptions.ConnectionOptions.TLSDisabled = true
		}
		if cfg.Temporal.APIKey != "" {
			temporalOptions.Credentials = client.NewAPIKeyStaticCredentials(cfg.Temporal.APIKey)
		}
		temporalClient, err = client.DialContext(context.Background(), temporalOptions)
		if err != nil {
			_ = dbClose(db)
			return nil, err
		}
		workerOptions, optionsErr := workerruntime.WorkerOptions(workerruntime.VersioningConfig{
			Enabled: cfg.Temporal.Versioning, DeploymentName: cfg.Temporal.DeploymentName,
			BuildID: cfg.Temporal.BuildID, Policy: cfg.Temporal.Policy,
		})
		if optionsErr != nil {
			temporalClient.Close()
			_ = dbClose(db)
			return nil, optionsErr
		}
		temporalWorker = sdkworker.New(temporalClient, cfg.Temporal.TaskQueue, workerOptions)
		activities := &presaleworkflow.Activities{Approval: approval, PMS: pms}
		presaleworkflow.Register(temporalWorker, activities)
		temporalPort := presaleworkflow.Client{Temporal: temporalClient, TaskQueue: cfg.Temporal.TaskQueue}
		approval = temporalPort
		pms = temporalPort
	}
	return &App{db: db, worker: NewWorker(newOutboxStore(db), service, approval, pms, cfg), temporalClient: temporalClient, temporalWorker: temporalWorker, metrics: metrics, temporalConfig: cfg.Temporal}, nil
}

func (a *App) Run(ctx context.Context) error {
	if a.temporalWorker != nil {
		if err := workerruntime.StartMetricsServer(ctx, a.temporalConfig.MetricsAddress, a.metrics, log.Default()); err != nil {
			return err
		}
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
