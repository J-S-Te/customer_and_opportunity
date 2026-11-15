package portalprojectworker

import (
	"context"
	"fmt"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/databasepool"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db     *gorm.DB
	worker *Worker
}

// New 构造既有CRM项目同步器，启动前拒绝本地目录或冲突权威，并配置独立连接池预算。
// 参数：ctx 为进程装配上下文，cfg 为既有业务和外部依赖配置。
// 返回值：应用实例；配置或依赖初始化失败返回错误。
func New(ctx context.Context, cfg Config) (*App, error) {
	if err := validateDirectoryMode(cfg.LocalDirectoryMode); err != nil {
		return nil, err
	}
	source, err := newHTTPSource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("configure Portal project source: %w", err)
	}
	db, err := databasepool.Open(
		mysql.Open(cfg.MySQLDSN),
		&gorm.Config{DisableAutomaticPing: true},
		databasepool.Worker,
	)
	if err != nil {
		return nil, fmt.Errorf("open Portal project database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get Portal project database handle: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping Portal project database: %w", err)
	}
	if err := localidentity.NewRepository(db).CheckMode(ctx, cfg.TenantID, localidentity.ModeCRM); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &App{db: db, worker: newWorker(newStore(db), source, cfg)}, nil
}

func (a *App) Run(ctx context.Context) error { return a.worker.Run(ctx) }
func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
