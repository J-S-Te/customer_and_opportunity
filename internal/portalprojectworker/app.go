package portalprojectworker

import (
	"context"
	"fmt"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db     *gorm.DB
	worker *Worker
}

// New 构造既有CRM项目同步器，启动前拒绝本地目录或冲突权威。
// 参数：ctx为生命周期，cfg为固定来源配置。
// 返回值：真实同步器或来源、依赖错误。
func New(ctx context.Context, cfg Config) (*App, error) {
	if err := validateDirectoryMode(cfg.LocalDirectoryMode); err != nil {
		return nil, err
	}
	source, err := newHTTPSource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("configure Portal project source: %w", err)
	}
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
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
