package presaleengineersyncworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	db     *gorm.DB
	worker *Worker
}

func New(cfg Config) (*App, error) {
	source, err := NewHTTPSource(cfg)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
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
