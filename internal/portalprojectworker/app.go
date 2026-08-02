package portalprojectworker

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db     *gorm.DB
	worker *Worker
}

func New(ctx context.Context, cfg Config) (*App, error) {
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
