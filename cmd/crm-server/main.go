package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/bootstrap"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	config, err := bootstrap.LoadConfig()
	if err != nil {
		logger.Error("CRM configuration failed", "error", err)
		os.Exit(1)
	}
	manifest := platformcatalog.CRMManifest()
	if config.OIDCMaxRoles != manifest.Policy.MaxEffectiveRoles {
		logger.Error("CRM OIDC role policy is incompatible", "expected_max_roles", manifest.Policy.MaxEffectiveRoles)
		os.Exit(1)
	}
	if !config.DevelopmentAuth {
		if err = platformcatalog.ValidateClaimsRoleConfigHash(manifest, config.OIDCRoleConfigHash); err != nil {
			logger.Error("CRM authorization catalog is incompatible", "error", err)
			os.Exit(1)
		}
	}
	if err = platformcatalog.Publish(context.Background(), manifest, platformcatalog.Options{
		Enabled: config.CatalogSyncEnabled, BaseURL: config.PlatformBaseURL, ApplicationID: config.CatalogApplicationID,
		ClientID: config.CatalogClientID, ClientSecret: config.CatalogClientSecret,
	}); err != nil {
		logger.Error("CRM authorization catalog synchronization failed", "error", err)
		os.Exit(1)
	}
	app, err := bootstrap.New(config)
	if err != nil {
		logger.Error("CRM startup failed", "error", err)
		os.Exit(1)
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- app.Server.ListenAndServe() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-signals:
	case err = <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("CRM server failed", "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = app.Close(ctx); err != nil {
		logger.Error("CRM shutdown failed", "error", err)
	}
}
