package database

import (
	"context"

	"gorm.io/gorm"
)

type contextKey string

const transactionKey contextKey = "gorm_transaction"

func WithTransaction(ctx context.Context, db *gorm.DB, fn func(context.Context) error) error {
	// 将 GORM 事务句柄写入派生上下文，跨仓储调用无需暴露具体事务参数，并保证任一错误触发回滚。
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, transactionKey, tx))
	})
}

func FromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	// 仓储必须优先使用当前事务；回退连接只用于事务外查询，避免审计或子资源写入逃逸出外层事务。
	if tx, ok := ctx.Value(transactionKey).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	if fallback == nil {
		return nil
	}
	return fallback.WithContext(ctx)
}

// 将已打开的事务接入统一仓储约定，供应用层批处理复用；派生上下文只能在该事务生命周期内使用。
func WithHandle(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, transactionKey, tx)
}
