package main

import (
	"context"
	"errors"
	"flag"
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
	if *runOnce {
		// 单次模式供发布任务和人工补算使用：沿用同一幂等聚合路径，完成一轮后主动退出。
		if err = app.RunOnce(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err = app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
