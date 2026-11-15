package presaleengineersyncworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/databasepool"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db     *gorm.DB
	worker *Worker
}

// New 装配当前模块的长期任务并配置独立连接池预算。
// 参数：cfg 为既有业务和外部依赖配置。
// 返回值：应用实例；配置或依赖初始化失败返回错误。
func New(cfg Config) (*App, error) {
	source, err := NewHTTPSource(cfg)
	if err != nil {
		return nil, err
	}
	db, err := databasepool.Open(
		mysql.Open(cfg.MySQLDSN),
		&gorm.Config{},
		databasepool.Worker,
	)
	if err != nil {
		return nil, err
	}
	codec, err := security.NewSensitiveCodec(cfg.EncryptionKey, cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	ids := func() string {
		raw := make([]byte, 16)
		if _, readErr := rand.Read(raw); readErr != nil {
			panic(readErr)
		}
		return hex.EncodeToString(raw)
	}
	return &App{db: db, worker: NewWorker(NewStore(db, codec, ids), source, cfg)}, nil
}
func (a *App) Run(ctx context.Context) error { return a.worker.Run(ctx) }
func (a *App) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
