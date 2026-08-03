package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/contracttransferworker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := contracttransferworker.LoadConfig()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	app, err := contracttransferworker.New(cfg)
	if err != nil {
		logger.Error("worker initialization failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			logger.Error("database close failed", "error", closeErr)
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	// Run 以取消上下文结束领取循环；context.Canceled 是正常停机，不应被容器判定为任务失败。
	if err = app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
