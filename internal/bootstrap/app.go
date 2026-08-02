package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/crmauth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/notification"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type App struct {
	Config Config
	DB     *gorm.DB
	Router *gin.Engine
	Server *http.Server
}

func New(config Config) (*App, error) {
	db, err := gorm.Open(mysql.Open(config.MySQLDSN), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return nil, err
	}
	codec, err := security.NewSensitiveCodec(config.EncryptionKey, config.HMACKey)
	if err != nil {
		return nil, err
	}
	router := gin.New()
	// RequestID must be outermost so both Recovery and AccessLog use the same
	// trace. AccessLog wraps Recovery and therefore observes the status and
	// bytes that were actually committed after a panic was handled.
	router.Use(middleware.RequestID(), middleware.AccessLog(slog.Default(), "crm", nil), middleware.Recovery(slog.Default(), "crm"))
	base := router.Group(strings.TrimRight(config.PathPrefix, "/"))
	base.GET("/healthz", func(c *gin.Context) {
		sqlDB, pingErr := db.DB()
		if pingErr != nil || sqlDB.PingContext(c.Request.Context()) != nil {
			c.JSON(503, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	var authMiddleware gin.HandlerFunc
	var internalAuthMiddleware gin.HandlerFunc
	if config.DevelopmentAuth {
		authMiddleware = middleware.DevelopmentAuth(true)
		internalAuthMiddleware = middleware.DevelopmentAuth(true)
	} else {
		oidcClient, oidcErr := crmauth.NewPlatformOIDCClient(context.Background(), crmauth.OIDCOptions{
			Issuer: config.OIDCIssuer, BackchannelBaseURL: config.OIDCBackchannelBaseURL,
			ClientID: config.OIDCClientID, ClientSecret: config.OIDCClientSecret,
			RedirectURI: config.OIDCRedirectURI, Scopes: config.OIDCScopes,
		})
		if oidcErr != nil {
			return nil, oidcErr
		}
		authService, authErr := crmauth.NewService(crmauth.NewGORMRepository(db), oidcClient, config.EncryptionKey, crmauth.Options{
			TenantID: config.OIDCTenantID, RoleConfigHash: config.OIDCRoleConfigHash,
			SessionTTL: config.OIDCSessionTTL, MaxRoles: config.OIDCMaxRoles,
		})
		if authErr != nil {
			return nil, authErr
		}
		authHandler := crmauth.NewHandler(authService, crmauth.HTTPOptions{
			PathPrefix: config.PathPrefix, PublicOrigin: config.PublicOrigin, CookieName: config.OIDCSessionCookieName,
			Issuer: config.OIDCIssuer, ClientID: config.OIDCClientID, CookieSecure: config.OIDCSessionSecure, PostLogoutRedirectURI: config.OIDCPostLogoutRedirectURI,
		})
		base.GET("/auth/login", authHandler.Login)
		base.GET("/auth/callback", authHandler.Callback)
		base.POST("/auth/logout", authHandler.RequireSameOrigin, authHandler.Logout)
		authMiddleware = middleware.SessionAuth(authService, config.OIDCSessionCookieName)
		machineAuth, machineErr := crmauth.NewMachineAuthenticator(context.Background(), db, crmauth.MachineOptions{Issuer: config.MachineTokenIssuer, Audience: config.MachineTokenAudience, PublicKeyPath: config.MachineTokenPublicKeyPath, TenantID: config.OIDCTenantID})
		if machineErr != nil {
			return nil, machineErr
		}
		internalAuthMiddleware = middleware.MachineAuth(machineAuth)
	}
	apiMiddlewares := []gin.HandlerFunc{presale.ContactPhoneNoStore(), authMiddleware}
	if !config.DevelopmentAuth {
		apiMiddlewares = append(apiMiddlewares, middleware.RequireSameOriginWrite(config.PublicOrigin))
	}
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
		response.OK(c, gin.H{
			"user_id": principal.UserID, "person_id": principal.PersonID,
			"tenant_id": principal.TenantID, "display_name": principal.DisplayName,
			"primary_org_id": principal.PrimaryOrgID, "organization_ids": append([]string{}, principal.OrganizationIDs...),
			"roles": principal.Roles, "permissions": permissions, "scope_mode": principal.ScopeMode,
			"role_config_hash": principal.RoleConfigHash, "authz_revision": principal.AuthzRevision,
		})
	})
	workerReadiness := presale.NewGORMWorkerReadinessRepository(db)
	api.GET("/capabilities", runtimeCapabilitiesHandler(config, workerReadiness, config.PresaleWorkerHeartbeatMaxAge))
	var ownerCatalog ownerdirectory.Catalog = ownerdirectory.UnavailableCatalog{}
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
	auditWriter := audit.NewGORMWriter(db)
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
	customer.RegisterRoutes(api, customer.NewHandler(customerService))
	var portalInviteHandler *portalinvite.Handler
	if config.PortalInviteEnabled {
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
		portalInviteService := portalinvite.NewService(
			portalInviteRepo, portalCustomerAdapter, platformProvisioner,
			portalProvisioner, auditWriter,
			config.PortalInvitePepper, config.PortalPublicURL, portalinvite.SystemClock{}, portalinvite.CryptoRandom{}, portalInviteOperationProtector{codec: codec},
		)
		portalInviteHandler = portalinvite.NewHandler(portalInviteService)
		portalinvite.RegisterRoutes(api, portalInviteHandler)
		portalinvite.RegisterAccessDisableRoute(api, portalinvite.NewAccessDisableHandler(portalinvite.NewAccessDisableService(
			portalInviteRepo, portalCustomerAdapter, platformRoleRevoker, portalDisabler, auditWriter, portalinvite.SystemClock{}, portalinvite.CryptoRandom{},
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
	opportunityService := opportunity.NewService(db, opportunityRepo, auditWriter, contractVerifier)
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
	attachmentService := opportunity.NewAttachmentService(opportunity.NewGORMAttachmentRepository(db), opportunityRepo, auditWriter, opportunity.UnavailableAttachmentObjectStore{}, opportunity.UnavailableAttachmentScanner{}, 0)
	opportunityHandler := opportunity.NewHandler(opportunityService).UseStageAlerts(opportunity.NewStageAlertService(db)).UseAttachments(attachmentService)
	opportunity.RegisterRoutes(api, opportunityHandler)
	notification.RegisterRoutes(api, notification.NewHandler(notification.NewService(db)))
	presaleRepo := presale.NewGORMRepository(db)
	presaleService := presale.NewService(
		presaleRepo,
		presaleOpportunityReader{service: opportunityService},
		presalePhoneProtector{codec: codec},
		presale.SystemClock{},
		requestIDGenerator{},
	).UseTimelineCursorKey(config.HMACKey).UseAuditWriter(auditWriter).
		UseWorkerReadiness(workerReadiness, config.PresaleWorkerHeartbeatMaxAge)
	if config.ApprovalTaskResolverEnabled {
		approvalTaskResolver, resolverErr := presale.NewHTTPApprovalTaskResolver(context.Background(), presale.ApprovalTaskResolverOptions{
			Endpoint: config.ApprovalTaskURL, TokenURL: config.ApprovalTaskTokenURL,
			ClientID: config.ApprovalTaskClientID, ClientSecret: config.ApprovalTaskClientSecret,
			Scope: config.ApprovalTaskScope, TLS: config.ApprovalTaskTLS,
		})
		if resolverErr != nil {
			return nil, resolverErr
		}
		presaleService.UseApprovalTaskResolver(approvalTaskResolver)
	}
	presaleHandler := presale.NewHandler(presaleService, presale.NewAlertService(db, presale.SystemClock{}), presaleActorResolver{}).
		UseReports(presale.NewReportService(presaleRepo)).
		UseEngineers(presale.NewEngineerService(presale.NewGORMEngineerDirectoryRepository(db, requestIDGenerator{}), presale.SystemClock{}, requestIDGenerator{}))
	presale.RegisterRoutes(api, presaleHandler)
	opportunityPresales := opportunityPresaleHandler{opportunities: opportunityService, presales: presaleService}
	api.GET("/opportunities/:id/presale-requests", middleware.RequirePermission("opportunity.read"), opportunityPresales.List)
	internal := base.Group("/api/v1/internal", internalAuthMiddleware)
	if portalInviteHandler != nil {
		portalinvite.RegisterInternalRoutes(internal, portalInviteHandler)
	}
	opportunity.RegisterIntegrationRoutes(internal, opportunityHandler)
	presale.RegisterInternalRoutes(internal, presaleHandler)
	server := &http.Server{Addr: config.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	return &App{Config: config, DB: db, Router: router, Server: server}, nil
}

func (a *App) Close(ctx context.Context) error {
	shutdownErr := a.Server.Shutdown(ctx)
	sqlDB, dbErr := a.DB.DB()
	if dbErr == nil {
		dbErr = sqlDB.Close()
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return dbErr
}
