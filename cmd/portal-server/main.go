package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/platformcatalog"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/portalbootstrap"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shutdown"
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
	serverErrors := make(chan error, 1)
	// 缓冲通道保证退出信号到达后监听协程仍能上报结果而不泄漏。
	go func() { serverErrors <- app.Server.ListenAndServe() }()
	logger.Info("Portal API started", "address", config.Address, "path_prefix", config.PathPrefix)
	exitCode := 0
	select {
	case <-ctx.Done():
		// 信号只触发优雅停机：HTTP 在途请求与数据库释放都交给下面的 App.Close，
		// 避免先关数据库让仍在处理的请求失去事务连接。
	case err = <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Portal server failed", "error", err)
			exitCode = 1
		}
	}
	// 统一有界 join：整体停机预算默认 20s（CUSTOMER_OPPORTUNITY_SHUTDOWN_DRAIN_SECONDS 可覆盖）。
	// App.Close 内部按既有顺序先收敛 HTTP（10s），再取消后台并在既有 10s 内 join 审计投递器与预检；
	// 审计投递器只由 App.Close 取消，这里不另行 cancel，避免两处争抢关闭。
	budget := shutdown.DrainTimeout()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), budget)
	group := shutdown.NewGroup()
	var closeErr error
	group.Go("app-close", func() { closeErr = app.Close(drainCtx) })
	stuck := group.Join(drainCtx)
	cancelDrain()
	if len(stuck) > 0 {
		// 超预算不 panic：打印未退出组件名并以非零码退出，交由编排器重启并排查。
		logger.Error("Portal shutdown exceeded drain budget", "budget", budget.String(), "still_running", stuck)
		exitCode = 1
	} else {
		var componentErr *shutdown.StuckError
		if errors.As(closeErr, &componentErr) {
			logger.Error("Portal shutdown exceeded component drain budget", "budget", componentErr.Budget.String(), "still_running", componentErr.Components)
			exitCode = 1
		} else if closeErr != nil {
			logger.Error("Portal shutdown failed", "error", closeErr)
		}
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
