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

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/portalbootstrap"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	config, err := portalbootstrap.LoadConfig()
	if err != nil {
		logger.Error("Portal configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	manifest := platformcatalog.PortalManifest()
	// Portal 只接受与当前二进制权限目录完全一致的声明，防止平台目录更新与应用滚动发布
	// 不同步时把旧权限解释成新权限。
	if err = platformcatalog.ValidateClaimsRoleConfigHash(manifest, config.RoleConfigHash); err != nil {
		logger.Error("Portal authorization catalog is incompatible", "error", err)
		os.Exit(1)
	}
	if err = platformcatalog.Publish(ctx, manifest, platformcatalog.Options{
		Enabled: config.CatalogSyncEnabled, BaseURL: config.PlatformBaseURL, ApplicationID: config.CatalogApplicationID,
		ClientID: config.CatalogClientID, ClientSecret: config.CatalogClientSecret,
	}); err != nil {
		logger.Error("Portal authorization catalog synchronization failed", "error", err)
		os.Exit(1)
	}
	app, err := portalbootstrap.New(ctx, config)
	if err != nil {
		logger.Error("Portal startup failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		// Close 同时汇总 HTTP 与数据库关闭结果；此处属于进程退出清理，不能覆盖主运行错误。
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Close(closeCtx)
	}()
	go func() {
		<-ctx.Done()
		// 信号只触发优雅停机，不直接关闭数据库，避免仍在处理的请求失去事务连接。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Server.Shutdown(shutdownCtx)
	}()
	logger.Info("Portal API started", "address", config.Address, "path_prefix", config.PathPrefix)
	if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("Portal server failed", "error", err)
		os.Exit(1)
	}
}
