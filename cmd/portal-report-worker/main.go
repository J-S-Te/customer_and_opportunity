package main

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/portalreportworker"
)

func main() {
	cfg, err := portalreportworker.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	app, err := portalreportworker.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			log.Printf("close Portal report worker database: %v", closeErr)
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err = app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
