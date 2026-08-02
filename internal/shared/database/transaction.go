package database

import (
	"context"

	"gorm.io/gorm"
)

type contextKey string

const transactionKey contextKey = "gorm_transaction"

func WithTransaction(ctx context.Context, db *gorm.DB, fn func(context.Context) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, transactionKey, tx))
	})
}

func FromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(transactionKey).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	if fallback == nil {
		return nil
	}
	return fallback.WithContext(ctx)
}

// WithHandle exposes an already-open transaction to repositories that follow
// the shared context convention. It is intended for application-owned batch
// transactions; callers must not retain the returned context.
func WithHandle(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, transactionKey, tx)
}
