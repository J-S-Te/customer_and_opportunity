package main

import (
	"context"
	"errors"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shutdown"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"log"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/portalinvitecompensationworker"
)

func main() {
	cfg, err := portalinvitecompensationworker.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, err = workerlicense.Start(ctx, "customer_portal")
	if err != nil {
		log.Fatal("commercial license runtime configuration failed")
	}
	// 初始化也接收同一个取消上下文，启动阶段若收到终止信号，不再继续建立远端补偿依赖。
	app, err := portalinvitecompensationworker.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			log.Printf("close Portal invite compensation database: %v", closeErr)
		}
	}()
	// 停机顺序：信号取消后台循环 → 按预算 join；超预算时列出未退出组件并以非零码退出（不 panic）。
	if err = shutdown.RunWorker(ctx, "portal-invite-compensation-worker-loop", app.Run); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
