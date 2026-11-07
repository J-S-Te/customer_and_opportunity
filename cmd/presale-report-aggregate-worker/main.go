package main

import (
	"context"
	"errors"
	"flag"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shutdown"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"log"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/presalereportaggregateworker"
)

func main() {
	runOnce := flag.Bool("once", false, "run one aggregate pass and exit")
	flag.Parse()
	config, err := presalereportaggregateworker.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	app, err := presalereportaggregateworker.New(config)
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
	if *runOnce {
		// 单次模式供发布任务和人工补算使用：沿用同一幂等聚合路径，完成一轮后主动退出。
		if err = app.RunOnce(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	// 停机顺序：信号取消后台循环 → 按预算 join；超预算时列出未退出组件并以非零码退出（不 panic）。
	if err = shutdown.RunWorker(ctx, "presale-report-aggregate-worker-loop", app.Run); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
