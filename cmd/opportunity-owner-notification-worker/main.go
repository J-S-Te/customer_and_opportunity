package main

import (
	"context"
	"errors"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shutdown"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"log"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/ownerchangenotificationworker"
)

func main() {
	config, err := ownerchangenotificationworker.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	app, err := ownerchangenotificationworker.New(config)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			log.Printf("close database: %v", closeErr)
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, err = workerlicense.Start(ctx, "customer_and_opportunity")
	if err != nil {
		log.Fatal("commercial license runtime configuration failed")
	}
	// 停机顺序：信号取消后台循环 → 按预算 join；超预算时列出未退出组件并以非零码退出（不 panic）。
	if err = shutdown.RunWorker(ctx, "opportunity-owner-notification-worker-loop", app.Run); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
