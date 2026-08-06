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
	// 角色数量上限同时存在于平台目录策略和本地会话校验中；启动时对账可避免平台签发了
	// 本进程拒绝的声明，或本进程意外接受超出目录治理规则的角色集合。
	if config.OIDCMaxRoles != manifest.Policy.MaxEffectiveRoles {
		logger.Error("CRM OIDC role policy is incompatible", "expected_max_roles", manifest.Policy.MaxEffectiveRoles)
		os.Exit(1)
	}
	// 二进制内置角色—权限映射必须始终与基础平台声明哈希绑定，不存在本地授权降级路径。
	if err = platformcatalog.ValidateClaimsRoleConfigHash(manifest, config.OIDCRoleConfigHash); err != nil {
		logger.Error("CRM authorization catalog is incompatible", "error", err)
		os.Exit(1)
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
	// 缓冲通道保证进程收到退出信号后，即使主协程不再接收，监听协程也不会因上报结果而泄漏。
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
	// 先停止接收新请求并等待在途请求，再关闭数据库连接；超时后由进程退出兜底。
	if err = app.Close(ctx); err != nil {
		logger.Error("CRM shutdown failed", "error", err)
	}
}
