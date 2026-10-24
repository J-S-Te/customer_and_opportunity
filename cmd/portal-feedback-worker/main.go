package main

import (
	"context"
	"errors"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"log"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/portalfeedbackworker"
)

func main() {
	config, err := portalfeedbackworker.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	app, err := portalfeedbackworker.New(config)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			log.Printf("close Portal feedback worker database failed")
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, err = workerlicense.Start(ctx, "customer_portal")
	if err != nil {
		log.Fatal("commercial license runtime configuration failed")
	}
	if err = app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
