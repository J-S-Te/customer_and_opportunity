package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/commercial"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegateway"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/contractreference"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/credit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/crmauth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/handover"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/importprovision"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	licenseCancel    context.CancelFunc
	Config           Config
	DB               *gorm.DB
	Router           *gin.Engine
	Server           *http.Server
	auditCancel      context.CancelFunc
	auditDone        chan struct{}
	importSourceDone chan struct{}
}

// New 装配当前应用的配置、存储、认证及服务入口。
// 参数：config为当前应用运行配置。
// 返回值：装配后的应用或认证实例；配置与依赖装配失败时返回错误。
func New(config Config) (*App, error) {
	licenseCtx, licenseCancel := context.WithCancel(context.Background())
	started := false
	defer func() {
		if !started {
			licenseCancel()
		}
	}()
	licenseCheck, err := commercial.Start(licenseCtx, "customer_and_opportunity")
	if err != nil {
		return nil, err
	}
	// 这里仅完成连接池装配，健康检查再执行真实 Ping；这样依赖未就绪会体现在健康状态，
	// 而配置、密钥或协议适配器错误仍会让进程在开放端口前失败。
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	codec, err := security.NewSensitiveCodec(config.EncryptionKey, config.HMACKey)
	if err != nil {
		return nil, err
	}
	auditStore := requestaudit.NewStore(db)
	auditDispatcher, err := requestaudit.NewDispatcher(auditStore, requestaudit.DispatcherOptions{
		PlatformBaseURL: config.PlatformBaseURL, ClientID: config.PlatformAuditClientID,
		ClientSecret: config.PlatformAuditClientSecret, ApplicationCode: config.PlatformApplicationCode,
		EnvironmentCode: config.PlatformEnvironmentCode, WorkerID: config.PlatformAuditWorkerID,
		PollInterval: config.PlatformAuditPollInterval, BatchSize: config.PlatformAuditBatchSize,
	})
	if err != nil {
		return nil, err
	}
	router := gin.New()
	// RequestID 必须位于最外层，使恢复与访问日志共享同一追踪号；访问日志包住恢复中间件，
	// 因而能够记录 panic 被转换后实际写出的状态码和字节数。
	router.Use(
		middleware.RequestID(),
		middleware.RequestAudit(auditStore, middleware.RequestAuditOptions{
			TenantID: config.OIDCTenantID, ApplicationCode: config.PlatformApplicationCode,
			EnvironmentCode: config.PlatformEnvironmentCode, TrustedProxies: config.TrustedProxies,
		}),
		middleware.AccessLog(slog.Default(), "crm", nil),
		middleware.Recovery(slog.Default(), "crm"),
	)
	base := router.Group(strings.TrimRight(config.PathPrefix, "/"))
	base.Use(commercial.Middleware(licenseCheck, "crm", config.PathPrefix))
	databaseHealthy := func(ctx context.Context) bool {
		sqlDB, pingErr := db.DB()
		return pingErr == nil && sqlDB.PingContext(ctx) == nil
	}
	base.GET("/livez", livenessHandler)
	base.GET("/readyz", readinessHandler(databaseHealthy))
	base.GET("/healthz", func(c *gin.Context) {
		if !databaseHealthy(c.Request.Context()) {
			c.JSON(503, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	// 浏览器和机器调用使用两条独立的基础平台信任链：前者只认服务端 OIDC 会话 Cookie，
	// 后者只认平台应用令牌。不存在请求头身份或本地权限降级路径。
	var centralClient central.Client
	if config.CentralSessionEnabled {
		client, err := central.NewHTTPClient(config.PlatformBaseURL, config.CentralSessionPublicBaseURL, config.OIDCClientID, config.OIDCRedirectURI)
		if err != nil {
			return nil, err
		}
		centralClient = client
	}
	oidcClient, oidcErr := crmauth.NewPlatformOIDCClient(context.Background(), crmauth.OIDCOptions{
		CentralClient: centralClient,
		Issuer:        config.OIDCIssuer, BackchannelBaseURL: config.OIDCBackchannelBaseURL,
		PlatformBaseURL: config.PlatformBaseURL,
		ClientID:        config.OIDCClientID, ClientSecret: config.OIDCClientSecret,
		ApplicationCode: config.PlatformApplicationCode, EnvironmentCode: config.PlatformEnvironmentCode,
		RoleConfigHash:       config.OIDCRoleConfigHash,
		IdentityProviderHint: config.OIDCIDPHint,
		RedirectURI:          config.OIDCRedirectURI, Scopes: config.OIDCScopes,
	})
	if oidcErr != nil {
		return nil, oidcErr
	}
	brokerVerifier, brokerErr := crmauth.NewPlatformBrokerVerifier(crmauth.BrokerVerificationOptions{
		PlatformBaseURL: config.PlatformBaseURL, ApplicationCode: config.PlatformApplicationCode,
		EnvironmentCode: config.PlatformEnvironmentCode, Issuer: config.OIDCIssuer, ClientID: config.OIDCClientID,
	})
	if brokerErr != nil {
		return nil, brokerErr
	}
	authRepository := crmauth.NewGORMRepository(db)
	authService, authErr := crmauth.NewService(authRepository, oidcClient, config.EncryptionKey, crmauth.Options{
		CentralClient:      centralClient,
		CentralExpectation: central.Expectation{TenantID: config.OIDCTenantID, ClientID: config.OIDCClientID, ApplicationCode: config.PlatformApplicationCode, EnvironmentCode: config.PlatformEnvironmentCode},
		TenantID:           config.OIDCTenantID, RoleConfigHash: config.OIDCRoleConfigHash,
		EnvironmentCode: config.PlatformEnvironmentCode, SessionIdleTTL: config.OIDCSessionIdleTTL, SessionAbsoluteTTL: config.OIDCSessionAbsoluteTTL, MaxRoles: config.OIDCMaxRoles,
	}, brokerVerifier)
	if authErr != nil {
		return nil, authErr
	}
	authHandler := crmauth.NewHandler(authService, crmauth.HTTPOptions{
		PathPrefix: config.PathPrefix, PublicOrigin: config.PublicOrigin, CookieName: config.OIDCSessionCookieName,
		EndSessionEndpoint: oidcClient.EndSessionEndpoint(), ClientID: config.OIDCClientID, CookieSecure: config.OIDCSessionSecure, PostLogoutRedirectURI: config.OIDCPostLogoutRedirectURI,
	})
	authMiddleware := middleware.SessionAuth(authService, config.OIDCSessionCookieName)
	base.GET("/auth/login", authHandler.Login)
	base.GET("/auth/callback", authHandler.Callback)
	base.GET("/auth/session", authMiddleware, authHandler.SessionState)
	base.POST("/auth/activity", authMiddleware, authHandler.RequireSameOrigin, authHandler.CentralActivity)
	base.POST("/auth/logout", authMiddleware, authHandler.RequireSameOrigin, authHandler.Logout)
	// local-logout 由前端裸 fetch 调用且不带 X-CSRF-Token，因此走仅 Origin 精确校验的同源防护
	//（浏览器同源 POST 自动携带 Origin），阻断跨站盲注销；Origin 缺失同样拒绝。
	base.POST("/auth/local-logout", middleware.RequireOriginMatch(config.PublicOrigin), authHandler.LocalLogout)
	base.POST("/auth/backchannel-logout", crmauth.NewBackchannelLogoutHandler(oidcClient, authRepository, config.OIDCIssuer, config.OIDCClientID, config.OIDCBackchannelLogoutTTL).Handle)
	machineAuth, machineErr := crmauth.NewMachineAuthenticator(context.Background(), db, crmauth.MachineOptions{Issuer: config.MachineTokenIssuer, Audience: config.MachineTokenAudience, PublicKeyPath: config.MachineTokenPublicKeyPath, TenantID: config.OIDCTenantID})
	if machineErr != nil {
		return nil, machineErr
	}
	internalAuthMiddleware := middleware.MachineAuth(machineAuth)
	apiMiddlewares := []gin.HandlerFunc{presale.ContactPhoneNoStore(), authMiddleware, middleware.RequireSameOriginWrite(config.PublicOrigin)}
	api := base.Group("/api/v1", apiMiddlewares...)
	api.GET("/auth/me", func(c *gin.Context) {
		principal, ok := auth.FromContext(c.Request.Context())
		if !ok {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		permissions := make([]string, 0, len(principal.Permissions))
		for code := range principal.Permissions {
			permissions = append(permissions, code)
		}
		sort.Strings(permissions)
		var sessionContext *central.Context
		if config.CentralSessionEnabled {
			cookie, _ := c.Request.Cookie(config.OIDCSessionCookieName)
			if cookie == nil {
				response.Error(c, apperror.ErrUnauthenticated)
				return
			}
			state, err := authService.Lifecycle(c.Request.Context(), cookie.Value, false)
			if err != nil {
				if errors.Is(err, crmauth.ErrUnauthenticated) {
					response.Error(c, apperror.ErrUnauthenticated)
				} else {
					response.Error(c, apperror.Wrap(err, http.StatusServiceUnavailable, "COMMON_DEPENDENCY_UNAVAILABLE", "会话服务暂不可用"))
				}
				return
			}
			sessionContext = &state
		}
		data := gin.H{
			"session_context": sessionContext,
			"user_id":         principal.UserID, "person_id": principal.PersonID,
			"tenant_id": principal.TenantID, "display_name": principal.DisplayName, "username": principal.Username,
			"primary_org_id": principal.PrimaryOrgID, "organization_ids": append([]string{}, principal.OrganizationIDs...),
			"roles": principal.Roles, "permissions": permissions, "scope_mode": principal.ScopeMode,
			"role_config_hash": principal.RoleConfigHash, "authz_revision": principal.AuthzRevision,
		}
		// 追加可选会话空闲窗口字段，既有调用方可忽略；剩余时间取 min(最后活动+空闲, 绝对到期) 的非负毫秒。
		if principal.SessionIdleTimeout > 0 {
			data["idle_timeout_seconds"] = int(principal.SessionIdleTimeout / time.Second)
		}
		if !principal.SessionIdleDeadline.IsZero() {
			remaining := principal.SessionIdleDeadline.Sub(time.Now()).Milliseconds()
			if remaining < 0 {
				remaining = 0
			}
			data["idle_remaining_ms"] = remaining
		}
		response.OK(c, data)
	})
	// 浏览器显式活动上报：只续期空闲窗口，不改变绝对到期；沿用写请求的 Origin/CSRF 防护。
	api.POST("/auth/activity", authHandler.Activity)
	// 此管理端点复用现有的审计读取权限，
	// 有意与公开的存活和就绪探针分离，仅暴露
	// 调用方租户的本地 Outbox 聚合状态。
	api.GET("/audit-outbox/status", middleware.RequirePermission("customer.audit.read"), auditOutboxStatusHandler(auditStore, config))
	workerReadiness := presale.NewGORMWorkerReadinessRepository(db)
	api.GET("/capabilities", runtimeCapabilitiesHandler(config, workerReadiness, config.PresaleWorkerHeartbeatMaxAge))
	var ownerCatalog ownerdirectory.Catalog = ownerdirectory.UnavailableCatalog{}
	// 可选集成均采用显式的不可用适配器或启动时构造成功的真实适配器，不允许在请求期间
	// 因空指针或默认放行而改变授权结果。
	if config.OwnerDirectoryEnabled {
		ownerClient, ownerErr := ownerdirectory.NewHTTPClient(context.Background(), ownerdirectory.HTTPOptions{
			Endpoint: config.PlatformOwnerDirectoryURL, TokenURL: config.PlatformManagementTokenURL,
			ClientID: config.PlatformOwnerDirectoryClientID, ClientSecret: config.PlatformOwnerDirectorySecret,
			Scope: config.PlatformOwnerDirectoryScope, TLS: config.PlatformManagementTLS,
		})
		if ownerErr != nil {
			return nil, ownerErr
		}
		ownerCatalog = ownerClient
	}
	ownerdirectory.RegisterRoutes(api, ownerdirectory.NewHandler(ownerCatalog))
	auditWriter := audit.NewGORMWriter(db).UsePlatformOutbox(auditStore, config.PlatformEnvironmentCode)
	customerRepo := customer.NewGORMRepository(db)
	customerService := customer.NewService(db, customerRepo, auditWriter, codec)
	if config.OwnerDirectoryEnabled {
		customerService.UseOwnerDirectory(ownerCatalog)
	}
	if config.PortalProjectHistoryEnabled {
		projectHistoryReader, readerErr := customer.NewHTTPProjectHistoryReader(context.Background(), customer.ProjectHistoryReaderOptions{
			Endpoint: config.PortalProjectHistoryURL, TokenURL: config.PortalProjectHistoryTokenURL,
			ClientID: config.PortalProjectHistoryClientID, ClientSecret: config.PortalProjectHistoryClientSecret,
			Scope: config.PortalProjectHistoryScope,
		})
		if readerErr != nil {
			return nil, readerErr
		}
		customerService.UseProjectHistoryReader(projectHistoryReader)
	}
	if config.AttachmentFileGatewayMode != "legacy" {
		gatewayHTTPClient, gatewayErr := newAttachmentFileGatewayClient(config)
		if gatewayErr != nil {
			return nil, gatewayErr
		}
		customerService.UseImportFileGateway(customer.NewImportGatewayAdapter(gatewayHTTPClient, config.AttachmentFileGatewayApplication))
	}
	customer.RegisterRoutes(api, customer.NewHandler(customerService))
	creditHandler := credit.NewHandler(credit.NewService(db).UseOwnerDirectory(ownerCatalog))
	credit.RegisterRoutes(api, creditHandler)
	var portalInviteHandler *portalinvite.Handler
	if config.PortalInviteEnabled {
		// 邀请流程跨越平台用户、应用角色和 Portal 映射三个资源域。各动作使用不同的最小
		// OAuth scope，服务层通过持久化步骤和补偿任务处理无法跨库原子提交的部分成功。
		platformProvisioner, provisionErr := portalinvite.NewHTTPPlatformProvisioner(context.Background(), portalinvite.PlatformProvisionerOptions{
			ProvisionURL: config.PlatformExternalUserProvisionURL, RoleAssignURL: config.PlatformApplicationRoleAssignURL,
			TokenURL: config.PlatformManagementTokenURL, ApplicationCode: config.PlatformPortalApplicationCode,
			ProvisionClientID: config.PlatformExternalUserClientID, ProvisionClientSecret: config.PlatformExternalUserClientSecret,
			ProvisionScope: config.PlatformExternalUserScope, RoleClientID: config.PlatformRoleAssignClientID,
			RoleClientSecret: config.PlatformRoleAssignClientSecret, RoleScope: config.PlatformRoleAssignScope,
			TLS: config.PlatformManagementTLS,
		})
		if provisionErr != nil {
			return nil, provisionErr
		}
		portalProvisioner, provisionErr := portalinvite.NewHTTPPortalProvisioner(context.Background(), portalinvite.PortalProvisionerOptions{
			Endpoint: config.PortalProvisionURL, TokenURL: config.PortalProvisionTokenURL,
			ClientID: config.PortalProvisionClientID, ClientSecret: config.PortalProvisionClientSecret,
			Scope: config.PortalProvisionScope, TLS: config.PortalProvisionTLS,
		})
		if provisionErr != nil {
			return nil, provisionErr
		}
		portalDisabler, provisionErr := portalinvite.NewHTTPPortalMappingDisabler(context.Background(), portalinvite.PortalMappingDisablerOptions{
			Endpoint: config.PortalDisableURL, TokenURL: config.PortalProvisionTokenURL,
			ClientID: config.PortalDisableClientID, ClientSecret: config.PortalDisableClientSecret,
			Scope: config.PortalDisableScope, TLS: config.PortalProvisionTLS,
		})
		if provisionErr != nil {
			return nil, provisionErr
		}
		platformRoleRevoker, provisionErr := portalinvite.NewHTTPPlatformRoleRevoker(context.Background(), portalinvite.PlatformRoleRevokerOptions{
			Endpoint: config.PlatformApplicationRoleRevokeURL, TokenURL: config.PlatformManagementTokenURL,
			ClientID: config.PlatformRoleRevokeClientID, ClientSecret: config.PlatformRoleRevokeClientSecret,
			Scope: config.PlatformRoleRevokeScope, ApplicationCode: config.PlatformPortalApplicationCode, TLS: config.PlatformManagementTLS,
		})
		if provisionErr != nil {
			return nil, provisionErr
		}
		portalInviteRepo := portalinvite.NewGORMRepository(db)
		portalCustomerAdapter := portalinvite.NewCustomerAdapter(db, codec)
		var inviteServiceOptions []portalinvite.ServiceOption
		var disableServiceOptions []portalinvite.AccessDisableServiceOption
		if config.PortalMappingDualWrite {
			// Phase 2 双写：门户映射仍是权威，平台绑定失败不中断 saga，由对账路径补齐。
			bindingWriter, bindErr := portalinvite.NewHTTPPlatformBindingWriter(context.Background(), portalinvite.PlatformBindingWriterOptions{
				BaseURL: config.PlatformCustomerBindingBaseURL, TokenURL: config.PlatformManagementTokenURL,
				ClientID: config.PortalProvisionClientID, ClientSecret: config.PortalProvisionClientSecret,
				Scope: "portal_mapping_provision", ApplicationCode: config.PlatformPortalApplicationCode, TLS: config.PlatformManagementTLS,
			})
			if bindErr != nil {
				return nil, bindErr
			}
			bindingDisabler, disableBindErr := portalinvite.NewHTTPPlatformBindingDisabler(context.Background(), portalinvite.PlatformBindingDisablerOptions{
				BaseURL: config.PlatformCustomerBindingBaseURL, TokenURL: config.PlatformManagementTokenURL,
				ClientID: config.PortalDisableClientID, ClientSecret: config.PortalDisableClientSecret,
				Scope: "portal_mapping_disable", ApplicationCode: config.PlatformPortalApplicationCode, TLS: config.PlatformManagementTLS,
			})
			if disableBindErr != nil {
				return nil, disableBindErr
			}
			inviteServiceOptions = append(inviteServiceOptions, portalinvite.WithPlatformBindingWriter(bindingWriter))
			disableServiceOptions = append(disableServiceOptions, portalinvite.WithPlatformBindingDisabler(bindingDisabler))
			if config.PortalMappingPlatformOnly {
				// Phase 5 单写：门户映射表退役，平台绑定成为唯一权威，门户调用全部跳过。
				inviteServiceOptions = append(inviteServiceOptions, portalinvite.WithPlatformBindingOnly())
				disableServiceOptions = append(disableServiceOptions, portalinvite.WithPlatformOnlyDisable())
			}
		}
		portalInviteService := portalinvite.NewService(
			portalInviteRepo, portalCustomerAdapter, platformProvisioner,
			portalProvisioner, auditWriter,
			config.PortalInvitePepper, config.PortalPublicURL, portalinvite.SystemClock{}, portalinvite.CryptoRandom{}, portalInviteOperationProtector{codec: codec},
			inviteServiceOptions...,
		)
		portalInviteHandler = portalinvite.NewHandler(portalInviteService)
		portalinvite.RegisterRoutes(api, portalInviteHandler)
		portalinvite.RegisterAccessDisableRoute(api, portalinvite.NewAccessDisableHandler(portalinvite.NewAccessDisableService(
			portalInviteRepo, portalCustomerAdapter, platformRoleRevoker, portalDisabler, auditWriter, portalinvite.SystemClock{}, portalinvite.CryptoRandom{}, disableServiceOptions...,
		)))
	}
	opportunityRepo := opportunity.NewGORMRepository(db)
	var contractVerifier opportunity.ContractVerifier
	if config.ContractVerificationEnabled {
		contractVerifier, err = opportunity.NewHTTPContractVerifier(context.Background(), opportunity.ContractVerifierOptions{
			Endpoint: config.ContractSummaryURL, TokenURL: config.ContractSummaryTokenURL,
			ClientID: config.ContractSummaryClientID, ClientSecret: config.ContractSummaryClientSecret,
			Scope: config.ContractSummaryScope,
		})
		if err != nil {
			return nil, err
		}
	}
	opportunityCatalogService := opportunity.NewCatalogService(db, auditWriter)
	opportunityService := opportunity.NewService(db, opportunityRepo, auditWriter, contractVerifier).UseCatalog(opportunityCatalogService)
	if config.ContractSignedCountEnabled {
		counter, counterErr := opportunity.NewHTTPSignedContractCounter(context.Background(), opportunity.SignedContractCounterOptions{
			Endpoint: config.ContractSignedCountURL, TokenURL: config.ContractSignedCountTokenURL,
			ClientID: config.ContractSignedCountClientID, ClientSecret: config.ContractSignedCountClientSecret,
			Scope: config.ContractSignedCountScope,
		})
		if counterErr != nil {
			return nil, counterErr
		}
		opportunityService.UseSignedContractCounter(counter)
	}
	if config.OwnerDirectoryEnabled {
		opportunityService.UseOwnerDirectory(ownerCatalog)
	}
	if config.QBStatusQueryEnabled {
		reader, readerErr := opportunity.NewHTTPQBStatusReader(context.Background(), opportunity.QBStatusReaderOptions{
			Endpoint: config.QBStatusURL, TokenURL: config.QBStatusTokenURL,
			ClientID: config.QBStatusClientID, ClientSecret: config.QBStatusClientSecret,
			Scope: config.QBStatusScope, TLS: config.QBStatusTLS,
		})
		if readerErr != nil {
			return nil, readerErr
		}
		opportunityService.UseQBStatusReader(reader)
	}
	if config.QBLaunchEnabled {
		signer, signerErr := opportunity.NewExternalLaunchSigner(opportunity.ExternalLaunchSignerOptions{
			QuotationURL: config.QBQuotationPublicURL, BidURL: config.QBBidPublicURL,
			Key: config.QBLaunchSigningKey, TTL: config.QBLaunchTTL,
		})
		if signerErr != nil {
			return nil, signerErr
		}
		opportunityService.UseExternalLaunchSigner(signer)
	}
	var attachmentStore opportunity.AttachmentObjectStore = opportunity.UnavailableAttachmentObjectStore{}
	var attachmentScanner opportunity.AttachmentScanner = opportunity.UnavailableAttachmentScanner{}
	switch {
	case config.AttachmentS3Enabled:
		// S3 兼容对象存储优先于本地目录：ETag 版本绑定与流式摘要校验满足
		// 生产对象一致性要求；附件内容由进程内静态规则校验。
		s3Store, storeErr := opportunity.NewS3AttachmentObjectStore(opportunity.S3AttachmentOptions{
			Endpoint: config.AttachmentS3Endpoint, Region: config.AttachmentS3Region, Bucket: config.AttachmentS3Bucket,
			AccessKeyID: config.AttachmentS3AccessKeyID, SecretAccessKey: config.AttachmentS3SecretAccessKey,
			PathStyle: config.AttachmentS3PathStyle, Prefix: config.AttachmentS3Prefix,
		})
		if storeErr != nil {
			return nil, storeErr
		}
		attachmentStore = s3Store
		attachmentScanner = opportunity.NewCodeAttachmentScanner(s3Store)
	case config.AttachmentLocalEnabled:
		gatewayClient, storeErr := filegateway.NewLocalClient(config.AttachmentLocalRoot)
		if storeErr != nil {
			return nil, storeErr
		}
		localStore := opportunity.NewLocalFileGatewayStore(gatewayClient)
		attachmentStore = localStore
		attachmentScanner = opportunity.NewCodeAttachmentScanner(localStore)
	}
	attachmentService := opportunity.NewAttachmentService(opportunity.NewGORMAttachmentRepository(db), opportunityRepo, auditWriter, attachmentStore, attachmentScanner, 0)
	if config.AttachmentFileGatewayMode != "legacy" {
		gatewayHTTPClient, gatewayErr := newAttachmentFileGatewayClient(config)
		if gatewayErr != nil {
			return nil, gatewayErr
		}
		migration, migrationErr := opportunity.NewHTTPAttachmentGatewayMigration(gatewayHTTPClient, config.AttachmentFileGatewayApplication, opportunity.AttachmentGatewayMode(config.AttachmentFileGatewayMode))
		if migrationErr != nil {
			return nil, migrationErr
		}
		attachmentService.UseFileGatewayMigration(migration)
	}
	opportunityHandler := opportunity.NewHandler(opportunityService).UseStageAlerts(opportunity.NewStageAlertService(db)).UseAttachments(attachmentService).UseOutboxAdmin(opportunityRepo)
	opportunity.RegisterRoutes(api, opportunityHandler)
	notification.RegisterRoutes(api, notification.NewHandler(notification.NewService(db)))
	presaleRepo := presale.NewGORMRepository(db)
	presaleService := presale.NewService(
		presaleRepo,
		presaleOpportunityReader{service: opportunityService},
		presalePhoneProtector{codec: codec},
		presale.SystemClock{},
		requestIDGenerator{},
	).UseTimelineCursorKey(config.HMACKey).UseAuditWriter(auditWriter).UseOwnerDirectory(ownerCatalog).UseApprovalRuleStore(presale.NewApprovalRuleStore(db)).UseWorkflowNotifications(presaleNotificationWriter{db: db})
	presaleHandler := presale.NewHandler(presaleService, presale.NewAlertService(db, presale.SystemClock{}), presaleActorResolver{}).
		UseReports(presale.NewReportService(presaleRepo)).
		UseApprovalRules(presale.NewApprovalRuleStore(db))
	presale.RegisterRoutes(api, presaleHandler)
	opportunityPresales := opportunityPresaleHandler{opportunities: opportunityService, presales: presaleService}
	api.GET("/opportunities/:id/presale-requests", middleware.RequirePermission("opportunity.read"), opportunityPresales.List)
	internal := base.Group("/api/v1/internal", internalAuthMiddleware)
	// 内部接口在独立路由组统一完成机器身份认证，具体处理器仍按 scope 和资源归属做二次约束。
	if portalInviteHandler != nil {
		portalinvite.RegisterInternalRoutes(internal, portalInviteHandler)
	}
	portalreportreference.RegisterRoutes(internal, portalreportreference.GORMReader{DB: db})
	contractreference.RegisterInternalRoutes(internal, contractreference.NewHandler(contractreference.NewService(contractreference.NewGORMRepository(db))))
	// 历史合同导入开立通道：客户与商机分别复用各自服务的 provision 入口
	//（负责人目录校验、阶段状态机与合同验真闸门仍由领域服务执行），此处只装配机器端点。
	importprovision.RegisterInternalRoutes(internal, importprovision.NewHandler(importprovision.NewService(customerService, opportunityService)))
	opportunity.RegisterIntegrationRoutes(internal, opportunityHandler)
	presale.RegisterInternalRoutes(internal, presaleHandler)
	credit.RegisterInternalRoutes(internal, creditHandler)
	// 人员离职交接机器协议：责任冻结、权威责任清单与逐项转移（机器身份鉴权，租户来自令牌）。
	handoverRepository, handoverErr := handover.NewGORMRepository(db)
	if handoverErr != nil {
		return nil, handoverErr
	}
	handoverRepository.UseOwnerOrgResolver(handoverOwnerOrgResolver{catalog: ownerCatalog})
	handoverService, handoverErr := handover.NewService(handoverRepository, handover.NewID, time.Now)
	if handoverErr != nil {
		return nil, handoverErr
	}
	// 交接机器协议路径四端统一为 /internal/v1/handover/*（平台编排器的固定后缀，
	// 基址只包含本服务的 APP_PATH_PREFIX），因此单独挂一组内部路由；
	// 认证/许可中间件与 /api/v1/internal 完全一致，scope 校验在模块内完成。
	handover.RegisterInternalRoutes(base.Group("/internal/v1", internalAuthMiddleware), handover.NewHandler(handoverService))
	// 审批责任必须由审批引擎确认当前审批人；未配置引擎时审批类责任不会出现在清单中。
	if config.ApprovalTaskResolverEnabled {
		approvalTasks, approvalErr := presale.NewHTTPApprovalTaskResolver(context.Background(), presale.ApprovalTaskResolverOptions{
			Endpoint: config.ApprovalTaskURL, TokenURL: config.ApprovalTaskTokenURL,
			ClientID: config.ApprovalTaskClientID, ClientSecret: config.ApprovalTaskClientSecret,
			Scope: config.ApprovalTaskScope, TLS: config.ApprovalTaskTLS,
		})
		if approvalErr != nil {
			return nil, approvalErr
		}
		presaleService.UseApprovalTaskResolver(approvalTasks)
		handoverService.SetApprovalResolver(handoverApprovalResolver{resolver: approvalTasks})
	}
	// 写路径冻结覆盖：交接中的用户不得再被指派为客户/商机负责人。
	// 冻结强制为 fail-closed（未接线即拒绝责任写入），因此装配必须自证接线生效。
	if wiringErr := wireHandoverFreezeEnforcement(customerService, opportunityService, presaleService, handoverRepository); wiringErr != nil {
		return nil, wiringErr
	}
	server := &http.Server{Addr: config.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	auditContext, auditCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		auditDispatcher.Run(auditContext)
	}()
	var importSourceDone chan struct{}
	if config.AttachmentFileGatewayMode != "legacy" {
		importSourceDone = make(chan struct{})
		go func() {
			defer close(importSourceDone)
			runCustomerImportSources(auditContext, config.OIDCTenantID, customerService, licenseCheck)
		}()
	}
	// 预检采用异步只读方式。平台
	// 不可用或审计发布者绑定错误必须在日志中明确呈现，
	// 但不得阻止 CRM 处理业务流量；本地 Outbox
	// 会保留事件，直至投递恢复。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := auditDispatcher.ValidateConfiguration(ctx); err != nil {
			slog.Default().Warn("platform audit publisher preflight failed", "error_code", requestaudit.DeliveryErrorCode(err))
		}
	}()
	started = true
	return &App{Config: config, DB: db, Router: router, Server: server, auditCancel: auditCancel, auditDone: auditDone, licenseCancel: licenseCancel, importSourceDone: importSourceDone}, nil
}

// handoverFreezeSource 是交接仓储向三条责任写入路径暴露的最小冻结查询能力。
// 接口方法集与 customer/opportunity/presale 各自的 FreezeChecker 完全一致。
type handoverFreezeSource interface {
	ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error)
}

// wireHandoverFreezeEnforcement 把交接冻结强制接入客户、商机、售前三条责任写入路径，
// 并自证接线生效：冻结检查 fail-closed，漏接任何一个写入方都会让该模块的所有责任写入被拒绝，
// 所以这里在组装期直接失败，而不是留到运行期变成 500。
func wireHandoverFreezeEnforcement(customerService *customer.Service, opportunityService *opportunity.Service, presaleService *presale.Service, source handoverFreezeSource) error {
	if customerService == nil || opportunityService == nil || presaleService == nil || source == nil {
		return errors.New("handover freeze enforcement requires all responsibility writers and a freeze source")
	}
	customerService.UseFreezeChecker(source)
	opportunityService.UseFreezeChecker(source)
	presaleService.UseFreezeChecker(source)
	for _, writer := range []struct {
		name     string
		enforced bool
	}{
		{"customer", customerService.HandoverFreezeEnforced()},
		{"opportunity", opportunityService.HandoverFreezeEnforced()},
		{"presale", presaleService.HandoverFreezeEnforced()},
	} {
		if !writer.enforced {
			return fmt.Errorf("handover freeze enforcement is not wired for %s responsibility writes", writer.name)
		}
	}
	return nil
}

func (a *App) Close(ctx context.Context) error {
	if a.licenseCancel != nil {
		a.licenseCancel()
	}
	// 先让 HTTP 在途请求收敛，再关闭共享连接池，避免请求持有的事务在优雅停机期间被截断。
	shutdownErr := a.Server.Shutdown(ctx)
	if a.auditCancel != nil {
		a.auditCancel()
	}
	if a.auditDone != nil {
		select {
		case <-a.auditDone:
		case <-ctx.Done():
			shutdownErr = errors.Join(shutdownErr, ctx.Err())
		}
	}
	if a.importSourceDone != nil {
		select {
		case <-a.importSourceDone:
		case <-ctx.Done():
			shutdownErr = errors.Join(shutdownErr, ctx.Err())
		}
	}
	sqlDB, dbErr := a.DB.DB()
	if dbErr == nil {
		dbErr = sqlDB.Close()
	}
	return errors.Join(shutdownErr, dbErr)
}
