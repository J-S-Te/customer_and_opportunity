package portalbootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/capability"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/evaluation"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/feedback"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/filing"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectexport"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectmessage"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

const (
	sessionContextKey  = "portal_session"
	maxPortalQueryPage = 1_000_000
)

type RouterDependencies struct {
	Config                Config
	RequestAudit          gin.HandlerFunc
	Account               *account.Service
	ProvisionAccount      func(context.Context, account.ProvisionCommand) (account.ProvisionResult, error)
	DisableAccount        func(context.Context, account.DisableCommand) (account.DisableResult, error)
	ReconcileAccounts     func(context.Context, string, []string) ([]account.ReconciliationSnapshot, error)
	Projects              *project.Service
	ProjectExports        *projectexport.Service
	ProjectMessages       *projectmessage.Service
	Reports               *report.Service
	ReportDownloads       *report.DownloadService
	WorkerReadiness       workerruntime.Readiness
	WorkerHeartbeatMaxAge time.Duration
	ReportDownloadError   func(context.Context, error)
	Feedback              *feedback.Service
	Evaluations           *evaluation.Service
	Filings               *filing.Service
	FilingMaterials       *filing.MaterialService
	MachineAuthenticator  machineAuthenticator
	CustomerCapabilities  capability.Reader
	DatabaseHealthy       func() bool
	Logger                *slog.Logger
}

type runtimeCapability struct {
	Available  bool   `json:"available"`
	Mode       string `json:"mode"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type runtimeCapabilities struct {
	ReportRequestSubmission runtimeCapability    `json:"report_request_submission"`
	ProjectExport           runtimeCapability    `json:"project_export"`
	ReportDownload          runtimeCapability    `json:"report_download"`
	FilingMaterialUpload    runtimeCapability    `json:"filing_material_upload"`
	FilingExport            runtimeCapability    `json:"filing_export"`
	FilingPoliceSubmission  runtimeCapability    `json:"filing_police_submission"`
	Customer                customerCapabilities `json:"customer"`
}

type customerCapabilities struct {
	ProjectEnabled    bool `json:"project_enabled"`
	ReportEnabled     bool `json:"report_enabled"`
	FilingEnabled     bool `json:"filing_enabled"`
	FeedbackEnabled   bool `json:"feedback_enabled"`
	EvaluationEnabled bool `json:"evaluation_enabled"`
}

func capabilityOptionsToResponse(options capability.Options) customerCapabilities {
	return customerCapabilities{
		ProjectEnabled: options.ProjectEnabled, ReportEnabled: options.ReportEnabled,
		FilingEnabled: options.FilingEnabled, FeedbackEnabled: options.FeedbackEnabled,
		EvaluationEnabled: options.EvaluationEnabled,
	}
}

type machineAuthenticator interface {
	Authenticate(context.Context, *http.Request) (sharedauth.Principal, error)
}

func NewRouter(deps RouterDependencies) *gin.Engine {
	logger := deps.Logger
	if logger == nil {
		// 仅路由测试和嵌入方未显式提供日志器时保持静默；生产装配始终注入结构化日志器。
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	router := gin.New()
	configureOpaquePathRouting(router)
	globalMiddleware := []gin.HandlerFunc{middleware.RequestID()}
	if deps.RequestAudit != nil {
		globalMiddleware = append(globalMiddleware, deps.RequestAudit)
	}
	globalMiddleware = append(globalMiddleware,
		middleware.AccessLog(logger, "portal", portalAccessLogActor),
		middleware.Recovery(logger, "portal"),
		secureHeaders(),
	)
	router.Use(globalMiddleware...)
	base := router.Group(deps.Config.PathPrefix)
	base.GET("/livez", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "alive"})
	})
	base.GET("/readyz", func(c *gin.Context) {
		if deps.DatabaseHealthy == nil || !deps.DatabaseHealthy() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	base.GET("/healthz", func(c *gin.Context) {
		if deps.DatabaseHealthy != nil && !deps.DatabaseHealthy() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	base.GET("/auth/login", beginLogin(deps))
	base.GET("/activate", func(c *gin.Context) {
		token := strings.TrimSpace(c.Query("token"))
		if token == "" {
			response.Error(c, account.ErrInvalidLoginState)
			return
		}
		result, err := deps.Account.BeginInvitationLogin(c.Request.Context(), token, deps.Config.PathPrefix+"/")
		if err != nil {
			response.Error(c, err)
			return
		}
		c.Redirect(http.StatusFound, result.AuthorizationURL)
	})
	base.GET("/auth/callback", completeLogin(deps))
	base.POST("/auth/logout", authenticate(deps), originAndCSRF(deps.Config), logout(deps))
	internal := base.Group("/internal", machineAuth(deps.MachineAuthenticator))
	internal.POST("/accounts/provision", requireMachineScope("portal.identity_mapping.provision"), requireMachineClientSubject(deps.Config.CRMProvisionClientSubject), provision(deps))
	internal.POST("/accounts/reconciliation-snapshot", requireMachineScope("portal.identity_mapping.provision"), requireMachineClientSubject(deps.Config.CRMProvisionClientSubject), reconciliationSnapshot(deps))
	internal.POST("/accounts/disable", requireMachineScope("portal.identity_mapping.disable"), requireMachineClientSubject(deps.Config.CRMDisableClientSubject), disableAccount(deps))
	internal.GET("/customers/:customerID/capabilities", requireMachineScope("portal.customer_capabilities.read"), getCustomerCapabilities(deps))
	internal.PUT("/customers/:customerID/capabilities", requireMachineScope("portal.customer_capabilities.manage"), updateCustomerCapabilities(deps))
	internal.POST("/report-callbacks", requireMachineScope("report.callback.write"), reportCallback(deps))
	internal.GET("/report-risk-alerts", requireMachineScope("portal.report.risk.manage"), listReportRiskAlertsForReview(deps))
	internal.POST("/report-risk-alerts/:id/review", requireMachineScope("portal.report.risk.manage"), reviewReportRiskAlert(deps))
	internal.GET("/feedbacks", requireMachineScope("portal.feedback.manage"), listFeedbackForOperator(deps))
	internal.GET("/evaluations/statistics", requireMachineScope("portal.evaluation.read"), evaluationStatistics(deps))
	internal.GET("/evaluations/low-score-notices", requireMachineScope("portal.evaluation.read"), listEvaluationNotices(deps))
	internal.POST("/evaluations/:id/low-score-notice/read", requireMachineScope("portal.evaluation.read"), readEvaluationNotice(deps))
	internal.POST("/filings/:id/unlock", requireMachineScope("portal.filing.unlock"), unlockFiling(deps))
	internal.POST("/filing-material-scan-callbacks", requireMachineScope("portal.filing_material.scan.write"), filingMaterialScanCallback(deps))
	internal.GET("/customers/:customerID/projects", requireMachineScope("portal.project_history.read"), listProjectHistoryForCRM(deps))
	internal.POST("/project-access/sync", requireMachineScope("portal.project_access.sync"), syncProjectAccess(deps))
	internal.GET("/project-conversations", requireMachineScope("portal.project_message.manage"), listProjectConversationsForManager(deps))
	internal.GET("/project-conversations/:id", requireMachineScope("portal.project_message.manage"), getProjectConversationForManager(deps))
	internal.POST("/project-conversations/:id/messages", requireMachineScope("portal.project_message.manage"), sendProjectMessageForManager(deps))
	internal.POST("/project-conversations/:id/read", requireMachineScope("portal.project_message.manage"), readProjectMessagesForManager(deps))
	for _, action := range []string{"accept", "respond", "request-info", "note", "resolve", "reject"} {
		internal.POST("/feedbacks/:id/"+action, requireMachineScope("portal.feedback.manage"), processFeedback(deps, action))
	}
	api := base.Group("/api/v1", authenticate(deps))
	api.GET("/auth/me", me)
	api.GET("/capabilities", capabilities(deps))
	api.GET("/account/security", requirePermission("account.security.manage"), accountSecurity(deps))
	api.GET("/account/sessions", requirePermission("account.security.manage"), accountSessions(deps))
	api.DELETE("/account/sessions/:id", requirePermission("account.security.manage"), originAndCSRF(deps.Config), revokeAccountSession(deps))
	api.POST("/account/security-events/:id/ack", requirePermission("account.security.manage"), originAndCSRF(deps.Config), acknowledgeSecurityEvent(deps))
	api.GET("/projects", requirePermission("project.read"), listProjects(deps))
	api.GET("/projects/:projectID", requirePermission("project.read"), getProject(deps))
	api.GET("/projects/:projectID/activities", requirePermission("project.read"), listActivities(deps))
	api.POST("/projects/:projectID/conversation", requirePermission("project.message.send"), originAndCSRF(deps.Config), createProjectConversation(deps))
	api.GET("/projects/:projectID/conversation", requirePermission("project.message.read"), getCurrentProjectConversation(deps))
	api.GET("/project-conversations/:id", requirePermission("project.message.read"), getProjectConversation(deps))
	api.POST("/project-conversations/:id/messages", requirePermission("project.message.send"), originAndCSRF(deps.Config), sendProjectMessage(deps))
	api.POST("/project-conversations/:id/read", requirePermission("project.message.read"), originAndCSRF(deps.Config), readProjectMessages(deps))
	api.GET("/projects/:projectID/evaluation-eligibility", requireAnyPermission("evaluation.read", "evaluation.create"), evaluationEligibility(deps))
	api.POST("/projects/:projectID/exports", requirePermission("project.export"), originAndCSRF(deps.Config), createProjectExport(deps))
	api.GET("/project-exports/:id", requirePermission("project.export"), getProjectExport(deps))
	api.POST("/project-exports/:id/download-grants", requirePermission("project.export"), originAndCSRF(deps.Config), createProjectExportGrant(deps))
	api.POST("/project-exports/:id/downloads", requirePermission("project.export"), originAndCSRF(deps.Config), downloadProjectExport(deps))
	api.GET("/reports", requirePermission("report.read"), listReports(deps))
	api.GET("/reports/:id", requirePermission("report.read"), getReport(deps))
	api.POST("/reports", requirePermission("report.request"), originAndCSRF(deps.Config), createReport(deps))
	api.GET("/report-requests", requirePermission("report.read"), listReports(deps))
	api.GET("/report-requests/:id", requirePermission("report.read"), getReport(deps))
	api.POST("/report-requests", requirePermission("report.request"), originAndCSRF(deps.Config), createReport(deps))
	api.POST("/reports/:id/download-grants", requirePermission("report.download"), originAndCSRF(deps.Config), createReportGrant(deps))
	api.POST("/report-requests/:id/download-grants", requirePermission("report.download"), originAndCSRF(deps.Config), createReportGrant(deps))
	api.POST("/reports/:id/downloads", requirePermission("report.download"), originAndCSRF(deps.Config), downloadReport(deps))
	api.POST("/report-requests/:id/downloads", requirePermission("report.download"), originAndCSRF(deps.Config), downloadReport(deps))
	api.GET("/report-notifications", requirePermission("report.read"), listReportNotifications(deps))
	api.GET("/report-notifications/unread-count", requirePermission("report.read"), reportNotificationUnreadCount(deps))
	api.POST("/report-notifications/:id/read", requirePermission("report.read"), originAndCSRF(deps.Config), readReportNotification(deps))
	api.GET("/report-risk-alerts", requirePermission("report.read"), listOwnedReportRiskAlerts(deps))
	api.GET("/feedbacks", requirePermission("feedback.read"), listFeedback(deps))
	api.GET("/feedbacks/:id", requirePermission("feedback.read"), getFeedback(deps))
	api.POST("/feedbacks", requirePermission("feedback.create"), originAndCSRF(deps.Config), createFeedback(deps))
	api.POST("/feedbacks/:id/messages", requirePermission("feedback.reply"), originAndCSRF(deps.Config), addFeedbackMessage(deps))
	api.POST("/feedbacks/:id/close", requirePermission("feedback.reply"), originAndCSRF(deps.Config), closeFeedback(deps))
	api.POST("/evaluations", requirePermission("evaluation.create"), originAndCSRF(deps.Config), submitEvaluation(deps))
	api.GET("/evaluations/:id", requirePermission("evaluation.read"), getEvaluation(deps))
	api.GET("/filings", requirePermission("filing.read"), listFilings(deps))
	api.POST("/filings", requirePermission("filing.create"), originAndCSRF(deps.Config), createFiling(deps))
	api.GET("/filings/:id", requirePermission("filing.read"), getFiling(deps))
	api.PUT("/filings/:id/sections/:code", requirePermission("filing.update"), originAndCSRF(deps.Config), saveFilingSection(deps))
	api.PUT("/filings/:id/matrix/:code", requirePermission("filing.update"), originAndCSRF(deps.Config), saveFilingMatrix(deps))
	api.POST("/filings/:id/validate", requirePermission("filing.read"), originAndCSRF(deps.Config), validateFiling(deps))
	api.POST("/filings/:id/submit", requirePermission("filing.submit"), originAndCSRF(deps.Config), submitFiling(deps))
	api.DELETE("/filings/:id", requirePermission("filing.delete"), originAndCSRF(deps.Config), deleteFiling(deps))
	api.POST("/filings/:id/material-uploads", requirePermission("filing.update"), originAndCSRF(deps.Config), createFilingMaterialUpload(deps))
	api.POST("/filings/:id/materials/:materialID/complete", requirePermission("filing.update"), originAndCSRF(deps.Config), completeFilingMaterialUpload(deps))
	api.POST("/filings/:id/exports", requirePermission("filing.read"), originAndCSRF(deps.Config), unsupported("PORTAL_FILING_EXPORT_NOT_CONFIGURED"))
	return router
}

func capabilities(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		value := runtimeCapabilities{
			ReportRequestSubmission: unavailableCapability("PORTAL_REPORT_DELIVERY_WORKER_UNAVAILABLE"),
			ProjectExport:           unavailableCapability("PORTAL_PROJECT_EXPORT_WORKER_UNAVAILABLE"),
			ReportDownload:          unavailableCapability("REPORT_SECURITY_PROVIDERS_NOT_CONFIGURED"),
			FilingMaterialUpload:    unavailableCapability("FILING_MATERIAL_PROVIDERS_NOT_CONFIGURED"),
			FilingExport:            unavailableCapability("FILING_EXPORT_NOT_CONFIGURED"),
			FilingPoliceSubmission:  runtimeCapability{Available: false, Mode: "LOCAL_ONLY", ReasonCode: "FILING_POLICE_SUBMISSION_CONTRACT_NOT_CONFIGURED"},
		}
		maxAge := deps.WorkerHeartbeatMaxAge
		if maxAge <= 0 {
			maxAge = workerruntime.HeartbeatMaxAge
		}
		if deps.WorkerReadiness != nil {
			notBefore := time.Now().UTC().Add(-maxAge)
			if ready, err := deps.WorkerReadiness.HasFreshHeartbeat(c.Request.Context(), workerruntime.ReportDeliveryWorker, notBefore); err == nil && ready {
				value.ReportRequestSubmission = runtimeCapability{Available: true, Mode: "READY"}
			}
			if ready, err := deps.WorkerReadiness.HasFreshHeartbeat(c.Request.Context(), workerruntime.ProjectExportWorker, notBefore); err == nil && ready {
				value.ProjectExport = runtimeCapability{Available: true, Mode: "READY"}
			}
		}
		if deps.ReportDownloads != nil && deps.ReportDownloads.RuntimeAvailable() {
			value.ReportDownload = runtimeCapability{Available: true, Mode: "READY"}
		}
		if deps.FilingMaterials != nil && deps.FilingMaterials.RuntimeAvailable() {
			value.FilingMaterialUpload = runtimeCapability{Available: true, Mode: "READY"}
		}
		if deps.CustomerCapabilities != nil {
			options, optionsErr := deps.CustomerCapabilities.Get(c.Request.Context(), deps.Config.TenantID, currentSession(c).CustomerID)
			if optionsErr != nil && deps.Logger != nil {
				deps.Logger.Error("customer capabilities read failed", "error", optionsErr)
			} else if optionsErr == nil {
				value.Customer = capabilityOptionsToResponse(options)
			}
		}
		response.OK(c, value)
	}
}

func unavailableCapability(reason string) runtimeCapability {
	return runtimeCapability{Available: false, Mode: "UNAVAILABLE", ReasonCode: reason}
}

func getCustomerCapabilities(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.CustomerCapabilities == nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_CUSTOMER_CAPABILITIES_UNAVAILABLE", "customer capabilities store is not configured"))
			return
		}
		customerID, err := strconv.ParseUint(c.Param("customerID"), 10, 64)
		if err != nil || customerID == 0 {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		options, err := deps.CustomerCapabilities.Get(c.Request.Context(), principal.TenantID, customerID)
		if err != nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_CUSTOMER_CAPABILITIES_UNAVAILABLE", "customer capabilities unavailable"))
			return
		}
		response.OK(c, capabilityOptionsToResponse(options))
	}
}

func updateCustomerCapabilities(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		writer, ok := deps.CustomerCapabilities.(capability.Writer)
		if !ok || writer == nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_CUSTOMER_CAPABILITIES_UNAVAILABLE", "customer capabilities store is not configured"))
			return
		}
		customerID, err := strconv.ParseUint(c.Param("customerID"), 10, 64)
		if err != nil || customerID == 0 {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		var body struct {
			Capabilities map[string]bool `json:"capabilities"`
		}
		if err := requestbody.DecodeJSON(c, &body); err != nil {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request body"))
			return
		}
		for key := range body.Capabilities {
			valid := false
			for _, allowed := range capability.AllKeys {
				if key == allowed {
					valid = true
					break
				}
			}
			if !valid {
				response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "unknown capability key"))
				return
			}
		}
		options := capability.OptionsFromMap(body.Capabilities)
		updated, err := writer.Upsert(c.Request.Context(), principal.TenantID, customerID, options)
		if err != nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_CUSTOMER_CAPABILITIES_UNAVAILABLE", "customer capabilities update failed"))
			return
		}
		response.OK(c, capabilityOptionsToResponse(updated))
	}
}

func portalAccessLogActor(c *gin.Context) (string, string) {
	session := currentSession(c)
	if session == nil {
		return "", ""
	}
	return session.TenantID, session.PlatformUserID
}

// 保留编码后的路径分隔符，使其仍属于一个不透明项目标识。处理器读取前继续叠加租户和客户范围；
// 浏览器必须编码标识中的斜杠，未编码斜杠仍会匹配成另一条路由结构。
func configureOpaquePathRouting(router *gin.Engine) {
	router.UseRawPath = true
	router.UnescapePathValues = true
}

func beginLogin(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		returnPath := c.Query("return_to")
		invite := c.Query("invite_token")
		var result account.LoginStart
		var err error
		if invite != "" {
			result, err = deps.Account.BeginInvitationLogin(c.Request.Context(), invite, returnPath)
		} else {
			result, err = deps.Account.BeginLogin(c.Request.Context(), deps.Config.TenantID, returnPath)
		}
		if err != nil {
			response.Error(c, err)
			return
		}
		c.Redirect(http.StatusFound, result.AuthorizationURL)
	}
}

func completeLogin(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Query("error") != "" {
			response.Error(c, account.ErrInvalidClaims)
			return
		}
		remoteIP := directRemoteIP(c.Request.RemoteAddr)
		result, err := deps.Account.CompleteLogin(c.Request.Context(), c.Query("state"), c.Query("code"), account.LoginMetadata{
			IPHash: hashText(deps.Config.HMACKey, remoteIP), IPMasked: maskIPAddress(remoteIP),
			Device: deviceSummary(c.Request.UserAgent()), UserAgentHash: hashText(deps.Config.HMACKey, c.Request.UserAgent()),
		})
		if err != nil {
			response.Error(c, err)
			return
		}
		http.SetCookie(c.Writer, sessionCookie(deps.Config, result.SessionToken, result.ExpiresAt))
		target := result.ReturnPath
		if !safeLocalPath(target, deps.Config.PathPrefix) {
			target = deps.Config.PathPrefix + "/"
		}
		c.Redirect(http.StatusFound, target)
	}
}

func logout(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := currentSession(c)
		cookie, _ := c.Request.Cookie(deps.Config.SessionCookieName)
		if cookie != nil {
			_ = deps.Account.Logout(c.Request.Context(), deps.Config.TenantID, session.PlatformUserID, cookie.Value)
		}
		expired := sessionCookie(deps.Config, "", time.Unix(1, 0))
		expired.MaxAge = -1
		http.SetCookie(c.Writer, expired)
		response.OK(c, gin.H{"logged_out": true})
	}
}

func authenticate(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Request.Cookie(deps.Config.SessionCookieName)
		if err != nil || cookie.Value == "" {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		// P1-3：陈旧授权窗口只放行只读方法；写请求必须在线复核授权。
		allowStale := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions
		session, err := deps.Account.AuthenticateSession(c.Request.Context(), deps.Config.TenantID, cookie.Value, allowStale)
		if err != nil {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		c.Set(sessionContextKey, session)
		if deps.CustomerCapabilities != nil {
			options, optionsErr := deps.CustomerCapabilities.Get(c.Request.Context(), session.TenantID, session.CustomerID)
			if optionsErr != nil {
				response.Error(c, apperror.ErrUnauthenticated)
				c.Abort()
				return
			}
			session.Permissions = capability.IntersectPermissions(session.Permissions, options)
		}
		permissions := make(map[string]struct{}, len(session.Permissions))
		for _, permission := range session.Permissions {
			permissions[permission] = struct{}{}
		}
		// 请求审计和业务处理共享同一个基础平台主体；Portal 本地会话只缓存平台已签名且
		// 周期性重验的声明，不创建第二套身份或权限来源。
		principal := sharedauth.Principal{
			UserID: session.PlatformUserID, TenantID: session.TenantID, LoginIP: session.LoginIP,
			Roles: append([]string(nil), session.Roles...), Permissions: permissions,
			DataScopes: portalDataScopes(session.DataScopes),
			ScopeMode:  sharedauth.ScopeSelf, RoleConfigHash: session.RoleConfigHash, AuthzRevision: session.AuthzRevision,
		}
		c.Request = c.Request.WithContext(sharedauth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

func me(c *gin.Context) {
	session := currentSession(c)
	response.OK(c, gin.H{
		"platform_user_id": session.PlatformUserID, "customer_id": session.CustomerID, "tenant_id": session.TenantID,
		"role_config_hash": session.RoleConfigHash, "authz_revision": session.AuthzRevision, "expires_at": session.ExpiresAt,
		"roles": session.Roles, "permissions": session.Permissions, "data_scopes": session.DataScopes,
	})
}

func portalDataScopes(scopes []account.DataScope) []sharedauth.DataScope {
	result := make([]sharedauth.DataScope, 0, len(scopes))
	for _, scope := range scopes {
		result = append(result, sharedauth.DataScope{RoleCode: scope.RoleCode, ScopeType: scope.ScopeType, ScopeID: scope.ScopeID, EnvironmentCode: scope.EnvironmentCode})
	}
	return result
}

func accountSecurity(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		value, err := deps.Account.AccountSecurity(c.Request.Context(), currentSession(c), configuredSecurityCenterURL(deps.Config))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func accountSessions(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := deps.Account.Sessions(c.Request.Context(), currentSession(c))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"items": items})
	}
}

func revokeAccountSession(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		current, err := deps.Account.RevokeOwnedSession(c.Request.Context(), currentSession(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		if current {
			expired := sessionCookie(deps.Config, "", time.Unix(1, 0))
			expired.MaxAge = -1
			http.SetCookie(c.Writer, expired)
		}
		response.OK(c, gin.H{"revoked": true, "current": current})
	}
}

func acknowledgeSecurityEvent(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := deps.Account.AcknowledgeSecurityEvent(c.Request.Context(), currentSession(c), c.Param("id")); err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"acknowledged": true})
	}
}

func configuredSecurityCenterURL(config Config) string {
	value, _ := url.Parse(config.AccountSecurityCenterURL)
	returnTo := strings.TrimSuffix(config.PublicOrigin, "/") + config.PathPrefix + "/security"
	query := value.Query()
	query.Set("return_to", returnTo)
	value.RawQuery = query.Encode()
	return value.String()
}

func maskIPAddress(value string) string {
	parsed := net.ParseIP(strings.TrimSpace(value))
	if parsed == nil {
		return ""
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return net.IPv4(ipv4[0], ipv4[1], 0, 0).String()
	}
	bytes := parsed.To16()
	for index := 6; index < len(bytes); index++ {
		bytes[index] = 0
	}
	return net.IP(bytes).String()
}

// 有意忽略转发头。若部署需要原始客户端地址，必须先配置明确的可信代理 CIDR；Gin 的宽松代理
// 默认值不能作为账号安全审计依据。
func directRemoteIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err == nil {
		return host
	}
	if net.ParseIP(strings.TrimSpace(remoteAddress)) != nil {
		return strings.TrimSpace(remoteAddress)
	}
	return ""
}

func deviceSummary(userAgent string) string {
	value := strings.ToLower(userAgent)
	osName := "未知系统"
	for _, rule := range []struct{ marker, label string }{{"iphone", "iPhone"}, {"ipad", "iPad"}, {"android", "Android"}, {"windows", "Windows"}, {"macintosh", "macOS"}, {"linux", "Linux"}} {
		if strings.Contains(value, rule.marker) {
			osName = rule.label
			break
		}
	}
	browser := "浏览器"
	for _, rule := range []struct{ marker, label string }{{"edg/", "Edge"}, {"chrome/", "Chrome"}, {"firefox/", "Firefox"}, {"safari/", "Safari"}} {
		if strings.Contains(value, rule.marker) {
			browser = rule.label
			break
		}
	}
	return osName + " · " + browser
}

func provision(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			TenantID       string  `json:"tenant_id"`
			AccountNo      string  `json:"account_no"`
			PlatformUserID string  `json:"platform_user_id"`
			DisplayName    string  `json:"display_name"`
			CustomerID     uint64  `json:"customer_id"`
			ContactID      *uint64 `json:"contact_id"`
		}
		if !decode(c, &body) {
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" || principal.TenantID != strings.TrimSpace(principal.TenantID) ||
			(body.TenantID != "" && body.TenantID != principal.TenantID) {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		provisionAccount := deps.ProvisionAccount
		if provisionAccount == nil {
			if deps.Account == nil {
				response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_ACCOUNT_PROVISION_UNAVAILABLE", "account provision service is unavailable"))
				return
			}
			provisionAccount = deps.Account.Provision
		}
		result, err := provisionAccount(c.Request.Context(), account.ProvisionCommand{TenantID: principal.TenantID, AccountNo: body.AccountNo, PlatformUserID: body.PlatformUserID, DisplayName: body.DisplayName, CustomerID: body.CustomerID, ContactID: body.ContactID})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, result)
	}
}

func reconciliationSnapshot(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Items []struct {
				PlatformUserID string `json:"platform_user_id"`
			} `json:"items"`
		}
		if !decode(c, &body) {
			return
		}
		if len(body.Items) == 0 || len(body.Items) > 100 {
			response.Error(c, apperror.New(http.StatusUnprocessableEntity, "COMMON_VALIDATION_ERROR", "items must contain between 1 and 100 identities"))
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		subjects := make([]string, 0, len(body.Items))
		for _, item := range body.Items {
			subjects = append(subjects, item.PlatformUserID)
		}
		read := deps.ReconcileAccounts
		if read == nil {
			if deps.Account == nil {
				response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_IDENTITY_RECONCILIATION_UNAVAILABLE", "identity reconciliation is unavailable"))
				return
			}
			read = deps.Account.ReconciliationSnapshots
		}
		items, err := read(c.Request.Context(), principal.TenantID, subjects)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"items": items})
	}
}

func disableAccount(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			TenantID       string `json:"tenant_id"`
			CustomerID     uint64 `json:"customer_id"`
			PlatformUserID string `json:"platform_user_id"`
			Reason         string `json:"reason"`
		}
		if !decode(c, &body) {
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" || principal.TenantID != strings.TrimSpace(principal.TenantID) ||
			(body.TenantID != "" && body.TenantID != principal.TenantID) || strings.TrimSpace(principal.UserID) == "" {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if idempotencyKey == "" || len(idempotencyKey) > 128 {
			response.Error(c, apperror.New(http.StatusUnprocessableEntity, "COMMON_VALIDATION_ERROR", "Idempotency-Key is required and must be at most 128 bytes"))
			return
		}
		disable := deps.DisableAccount
		if disable == nil {
			if deps.Account == nil {
				response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_ACCOUNT_DISABLE_UNAVAILABLE", "account disable service is unavailable"))
				return
			}
			disable = deps.Account.Disable
		}
		result, err := disable(c.Request.Context(), account.DisableCommand{
			TenantID: principal.TenantID, CustomerID: body.CustomerID, PlatformUserID: body.PlatformUserID,
			ActorID: principal.UserID, Reason: body.Reason, IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, result)
	}
}

func listProjects(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := currentSession(c)
		page, size, ok := bindProjectPagination(c, "status")
		if !ok {
			return
		}
		value, err := deps.Projects.List(c.Request.Context(), portalProjectScope(session), project.ListQuery{Status: c.Query("status"), Page: page, PageSize: size})
		if err != nil {
			response.Error(c, err)
			return
		}
		items := make([]projectSnapshotResponse, 0, len(value.Items))
		for i := range value.Items {
			items = append(items, publicProjectSnapshot(&value.Items[i]))
		}
		response.OK(c, gin.H{"items": items, "page": value.Page, "page_size": value.PageSize, "total": value.Total})
	}
}

func listProjectHistoryForCRM(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, pageSize, ok := bindProjectPagination(c)
		if !ok {
			return
		}
		customerID, err := strconv.ParseUint(c.Param("customerID"), 10, 64)
		if err != nil || customerID == 0 {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		if deps.Projects == nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_PROJECT_HISTORY_NOT_CONFIGURED", "project history provider is not configured"))
			return
		}
		value, err := deps.Projects.History(c.Request.Context(), project.Scope{TenantID: principal.TenantID, CustomerID: customerID}, project.ListQuery{Page: page, PageSize: pageSize}, time.Now().UTC(), deps.Config.projectHistoryStalenessThreshold())
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

// syncProjectAccess 接收项目系统按项目给出的完整 Portal 账号集合。
// 租户、客户和项目均在服务端通过项目快照再次确认，重复 source_version 请求安全重放。
func syncProjectAccess(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			TenantID      string    `json:"tenant_id"`
			CustomerID    uint64    `json:"customer_id"`
			ProjectID     string    `json:"project_id"`
			SourceVersion string    `json:"source_version"`
			AccountIDs    []string  `json:"account_ids"`
			UpdatedAt     time.Time `json:"updated_at"`
		}
		if !decode(c, &body) {
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(body.TenantID) != strings.TrimSpace(principal.TenantID) {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		if deps.Projects == nil {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "PORTAL_PROJECT_ACCESS_NOT_CONFIGURED", "project account binding store is not configured"))
			return
		}
		err := deps.Projects.SyncAccountBindings(c.Request.Context(), project.SyncAccountBindingsCommand{TenantID: body.TenantID, CustomerID: body.CustomerID, ProjectID: body.ProjectID, SourceVersion: body.SourceVersion, AccountIDs: body.AccountIDs, UpdatedAt: body.UpdatedAt})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"accepted": true})
	}
}
func getProject(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		session := currentSession(c)
		value, err := deps.Projects.Get(c.Request.Context(), portalProjectScope(session), c.Param("projectID"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicProjectBundle(value))
	}
}
func listActivities(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := currentSession(c)
		page, size, ok := bindProjectPagination(c)
		if !ok {
			return
		}
		value, err := deps.Projects.Activities(c.Request.Context(), portalProjectScope(session), c.Param("projectID"), page, size)
		if err != nil {
			response.Error(c, err)
			return
		}
		items := make([]projectActivityResponse, 0, len(value.Items))
		for i := range value.Items {
			items = append(items, publicActivity(&value.Items[i]))
		}
		response.OK(c, gin.H{"items": items, "page": value.Page, "page_size": value.PageSize, "total": value.Total})
	}
}

func projectExportActor(c *gin.Context) projectexport.Actor {
	session := currentSession(c)
	return projectexport.Actor{TenantID: session.TenantID, CustomerID: session.CustomerID, AccountID: session.PlatformUserID}
}

func createProjectExport(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		if deps.ProjectExports == nil {
			response.Error(c, projectexport.ErrNotFound)
			return
		}
		if !portalProjectVisible(c, deps, c.Param("projectID")) {
			return
		}
		value, err := deps.ProjectExports.Create(c.Request.Context(), projectExportActor(c), c.Param("projectID"), c.GetHeader("Idempotency-Key"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, gin.H{"export_id": value.PublicID, "status": value.Status, "source_updated_at": value.SourceUpdatedAt, "created_at": value.CreatedAt})
	}
}

func getProjectExport(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		if deps.ProjectExports == nil {
			response.Error(c, projectexport.ErrNotFound)
			return
		}
		value, err := deps.ProjectExports.Status(c.Request.Context(), projectExportActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"export_id": value.PublicID, "project_id": value.ProjectID, "status": value.Status, "failure_code": value.FailureCode, "source_updated_at": value.SourceUpdatedAt, "created_at": value.CreatedAt, "completed_at": value.CompletedAt})
	}
}

func createProjectExportGrant(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		if deps.ProjectExports == nil {
			response.Error(c, projectexport.ErrNotFound)
			return
		}
		value, err := deps.ProjectExports.CreateGrant(c.Request.Context(), projectExportActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, gin.H{"grant_id": value.GrantID, "expires_at": value.ExpiresAt, "download_token": value.DownloadToken})
	}
}

func downloadProjectExport(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		if deps.ProjectExports == nil {
			response.Error(c, projectexport.ErrNotFound)
			return
		}
		value, err := deps.ProjectExports.Download(c.Request.Context(), projectExportActor(c), c.Param("id"), c.GetHeader("X-Project-Export-Download-Token"))
		if err != nil {
			response.Error(c, err)
			return
		}
		c.Header("Content-Type", value.MIME)
		c.Header("Content-Disposition", contentDisposition(value.FileName))
		c.Header("Cache-Control", "no-store, private")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Content-SHA256", value.FileHash)
		hasher := sha256.New()
		written, writeErr := io.Copy(io.MultiWriter(c.Writer, hasher), bytes.NewReader(value.Bytes))
		success := writeErr == nil && written == int64(len(value.Bytes)) && strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), value.FileHash)
		reason := ""
		if !success {
			reason = "STREAM_INCOMPLETE"
		}
		completeCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
		defer cancel()
		if completeErr := value.Complete(completeCtx, success, reason); completeErr != nil {
			_ = c.Error(completeErr)
		}
	}
}

func bindProjectPagination(c *gin.Context, extraAllowed ...string) (int, int, bool) {
	allowed := append([]string{"page", "page_size"}, extraAllowed...)
	if !onlyProjectQueryKeys(c, allowed...) {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return 0, 0, false
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > maxPortalQueryPage {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page"))
		return 0, 0, false
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page size"))
		return 0, 0, false
	}
	if page > int(^uint(0)>>1)/pageSize {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page"))
		return 0, 0, false
	}
	return page, pageSize, true
}

func onlyProjectQueryKeys(c *gin.Context, allowed ...string) bool {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return false
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allow[key]; !ok || len(entries) != 1 {
			return false
		}
	}
	return true
}

func actor(c *gin.Context) report.Actor {
	s := currentSession(c)
	return report.Actor{TenantID: s.TenantID, CustomerID: s.CustomerID, AccountID: s.PlatformUserID}
}

// portalProjectScope 将平台下发的项目级数据范围传递到项目查询层。
// Portal 超管仍受当前租户和客户单位边界约束；普通账号只在平台明确下发项目范围
// 且本地存在对应绑定时收紧访问，未完成存量绑定迁移的账号继续沿用单位级回退策略。
func portalProjectScope(session *account.Session) project.Scope {
	if session == nil {
		return project.Scope{}
	}
	allowAll := false
	for _, role := range session.Roles {
		if role == "portal_super_admin" {
			allowAll = true
			break
		}
	}
	return project.Scope{TenantID: session.TenantID, CustomerID: session.CustomerID, AccountID: session.PlatformUserID, AllowAll: allowAll}
}

// portalProjectVisible 在所有接收 project_id 的写入入口复用项目服务端可见性校验，
// 防止攻击者绕过项目下拉框直接提交同单位其他项目标识。
func portalProjectVisible(c *gin.Context, deps RouterDependencies, projectID string) bool {
	if strings.TrimSpace(projectID) == "" || deps.Projects == nil {
		response.Error(c, apperror.New(http.StatusNotFound, "PORTAL_PROJECT_NOT_FOUND", "project not found"))
		return false
	}
	if _, err := deps.Projects.Get(c.Request.Context(), portalProjectScope(currentSession(c)), projectID); err != nil {
		response.Error(c, err)
		return false
	}
	return true
}
func listReports(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.Query("page"))
		size, _ := strconv.Atoi(c.Query("page_size"))
		value, err := deps.Reports.List(c.Request.Context(), actor(c), page, size)
		if err != nil {
			response.Error(c, err)
			return
		}
		items := make([]reportResponse, 0, len(value.Items))
		for i := range value.Items {
			items = append(items, publicReport(&value.Items[i]))
		}
		response.OK(c, gin.H{"items": items, "page": value.Page, "page_size": value.PageSize, "total": value.Total})
	}
}
func getReport(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		if id == 0 {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_VALIDATION_ERROR", "report id must be a positive integer"))
			return
		}
		value, err := deps.Reports.GetDetail(c.Request.Context(), actor(c), id)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicReportDetail(value))
	}
}
func createReport(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			ProjectID    string `json:"project_id"`
			ReportType   string `json:"report_type"`
			Reason       string `json:"reason"`
			ReceiveEmail string `json:"receive_email"`
		}
		if !decode(c, &body) {
			return
		}
		if body.ProjectID != "" {
			if _, err := deps.Projects.Get(c.Request.Context(), portalProjectScope(currentSession(c)), body.ProjectID); err != nil {
				response.Error(c, report.ErrProjectNotAccessible)
				return
			}
		}
		value, err := deps.Reports.Create(c.Request.Context(), actor(c), report.CreateCommand{ProjectID: body.ProjectID, ReportType: body.ReportType, Reason: body.Reason, ReceiveEmail: body.ReceiveEmail, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, publicReport(value))
	}
}

func createReportGrant(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		if id == 0 || deps.ReportDownloads == nil {
			response.Error(c, report.ErrNotFound)
			return
		}
		value, err := deps.ReportDownloads.CreateGrant(c.Request.Context(), actor(c), id, report.GrantCommand{
			IdempotencyKey: c.GetHeader("Idempotency-Key"), Metadata: reportDownloadMetadata(deps.Config, c),
		})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, gin.H{"grant_id": value.GrantID, "status": value.Status, "expires_at": value.ExpiresAt, "download_token": value.DownloadToken})
	}
}

func downloadReport(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		if id == 0 || deps.ReportDownloads == nil {
			response.Error(c, report.ErrNotFound)
			return
		}
		content, err := deps.ReportDownloads.AuthorizeDownload(c.Request.Context(), actor(c), id, c.GetHeader("X-Report-Download-Token"), reportDownloadMetadata(deps.Config, c))
		if err != nil {
			response.Error(c, err)
			return
		}
		defer content.Reader.Close()
		c.Header("Content-Type", content.MIME)
		c.Header("Content-Disposition", contentDisposition(content.FileName))
		c.Header("Cache-Control", "no-store, private")
		c.Header("X-Content-Type-Options", "nosniff")
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(c.Writer, hasher), io.LimitReader(content.Reader, content.Size+1))
		success := copyErr == nil && written == content.Size && strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), content.FileHash)
		reason := ""
		if !success {
			reason = "STREAM_INCOMPLETE"
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
		defer cancel()
		if completeErr := content.Complete(auditCtx, success, reason); completeErr != nil {
			// 响应体可能已经提交，不能再安全改写状态码；通过 Gin 错误链暴露审计/计数失败，供请求级
			// 可观测性记录，同时不记录凭据或对象引用。
			_ = c.Error(completeErr)
			if deps.ReportDownloadError != nil {
				deps.ReportDownloadError(auditCtx, completeErr)
			}
		}
	}
}

func reportDownloadMetadata(config Config, c *gin.Context) report.DownloadMetadata {
	return report.DownloadMetadata{
		IPHash:     hashText(config.HMACKey, directRemoteIP(c.Request.RemoteAddr)),
		DeviceHash: hashText(config.HMACKey, c.Request.UserAgent()),
	}
}

func listOwnedReportRiskAlerts(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, pageSize, ok := bindProjectPagination(c, "open_only")
		if !ok {
			return
		}
		openOnly := false
		if raw := c.Query("open_only"); raw != "" {
			var err error
			openOnly, err = strconv.ParseBool(raw)
			if err != nil {
				response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
				return
			}
		}
		if deps.ReportDownloads == nil {
			response.Error(c, report.ErrRiskAlertNotFound)
			return
		}
		value, err := deps.ReportDownloads.ListRiskAlerts(c.Request.Context(), actor(c), openOnly, page, pageSize)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func listReportRiskAlertsForReview(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, pageSize, ok := bindProjectPagination(c, "status")
		if !ok {
			return
		}
		principal, authenticated := sharedauth.FromContext(c.Request.Context())
		if !authenticated || deps.ReportDownloads == nil {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		value, err := deps.ReportDownloads.ListRiskAlertsForReview(c.Request.Context(), principal.TenantID, c.Query("status"), page, pageSize)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func reviewReportRiskAlert(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		principal, authenticated := sharedauth.FromContext(c.Request.Context())
		if !authenticated || deps.ReportDownloads == nil {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		var body struct {
			ExpectedVersion uint64 `json:"expected_version"`
			Action          string `json:"action"`
			Reason          string `json:"reason"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.ReportDownloads.ReviewRiskAlert(c.Request.Context(), principal.TenantID, principal.UserID, c.Param("id"), report.RiskReviewCommand{
			ExpectedVersion: body.ExpectedVersion, Action: body.Action, Reason: body.Reason,
			IdempotencyKey: c.GetHeader("Idempotency-Key"),
		})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func listReportNotifications(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, pageSize, ok := bindProjectPagination(c, "unread_only")
		if !ok {
			return
		}
		unreadOnly := false
		if raw := c.Query("unread_only"); raw != "" {
			var err error
			unreadOnly, err = strconv.ParseBool(raw)
			if err != nil {
				response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
				return
			}
		}
		value, err := deps.Reports.ListNotifications(c.Request.Context(), actor(c), unreadOnly, page, pageSize)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func reportNotificationUnreadCount(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !onlyProjectQueryKeys(c) {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
		value, err := deps.Reports.UnreadNotificationCount(c.Request.Context(), actor(c))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"count": value})
	}
}

func readReportNotification(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_VALIDATION_ERROR", "notification id must be a positive integer"))
			return
		}
		if err = deps.Reports.ReadNotification(c.Request.Context(), actor(c), id); err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"read": true})
	}
}

func contentDisposition(fileName string) string {
	// RFC 5987 编码避免 quoted-string 注入，ASCII 回退文件名不依赖回调返回的任意字符。
	return "attachment; filename=report.pdf; filename*=UTF-8''" + url.PathEscape(fileName)
}

type reportResponse struct {
	ID                  uint64        `json:"id"`
	RequestNo           string        `json:"request_no"`
	ProjectID           string        `json:"project_id"`
	ReportType          string        `json:"report_type"`
	Reason              string        `json:"reason"`
	Status              report.Status `json:"status"`
	DownstreamRequestID string        `json:"downstream_request_id,omitempty"`
	ApprovalResult      string        `json:"approval_result,omitempty"`
	SubmittedAt         time.Time     `json:"submitted_at"`
	ApprovedAt          *time.Time    `json:"approved_at,omitempty"`
	IssuedAt            *time.Time    `json:"issued_at,omitempty"`
	Version             uint64        `json:"version"`
}

type reportStatusEventResponse struct {
	EventType  string        `json:"event_type"`
	Sequence   uint64        `json:"sequence"`
	FromStatus report.Status `json:"from_status,omitempty"`
	ToStatus   report.Status `json:"to_status"`
	OccurredAt time.Time     `json:"occurred_at"`
}

type reportDetailResponse struct {
	reportResponse
	Events []reportStatusEventResponse `json:"events"`
}

func publicReport(value *report.Request) reportResponse {
	return reportResponse{ID: value.ID, RequestNo: value.RequestNo, ProjectID: value.ProjectID, ReportType: value.ReportType, Reason: value.Reason, Status: value.Status, DownstreamRequestID: value.DownstreamRequestID, ApprovalResult: value.ApprovalResult, SubmittedAt: value.SubmittedAt, ApprovedAt: value.ApprovedAt, IssuedAt: value.IssuedAt, Version: value.Version}
}
func publicReportDetail(value *report.Detail) reportDetailResponse {
	result := reportDetailResponse{reportResponse: publicReport(value.Request), Events: make([]reportStatusEventResponse, 0, len(value.Events))}
	for _, event := range value.Events {
		result.Events = append(result.Events, reportStatusEventResponse{EventType: event.EventType, Sequence: event.Sequence, FromStatus: event.FromStatus, ToStatus: event.ToStatus, OccurredAt: event.OccurredAt})
	}
	return result
}
func reportCallback(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body report.Callback
		if !decode(c, &body) {
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(body.TenantID) != strings.TrimSpace(principal.TenantID) {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		body.IdempotencyKey = c.GetHeader("Idempotency-Key")
		if err := deps.Reports.ApplyCallback(c.Request.Context(), body); err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, gin.H{"accepted": true})
	}
}

func feedbackActor(c *gin.Context) feedback.CustomerActor {
	session := currentSession(c)
	return feedback.CustomerActor{TenantID: session.TenantID, CustomerID: session.CustomerID, AccountID: session.PlatformUserID}
}

func filingActor(c *gin.Context) filing.Actor {
	session := currentSession(c)
	return filing.Actor{TenantID: session.TenantID, CustomerID: session.CustomerID, AccountID: session.PlatformUserID}
}

func listFilings(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		page, _ := strconv.Atoi(c.Query("page"))
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		value, err := deps.Filings.List(c.Request.Context(), filingActor(c), page, pageSize)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func createFiling(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		var body struct {
			ProjectID string `json:"project_id"`
		}
		if !decode(c, &body) {
			return
		}
		if body.ProjectID != "" {
			if _, err := deps.Projects.Get(c.Request.Context(), portalProjectScope(currentSession(c)), body.ProjectID); err != nil {
				response.Error(c, filing.ErrNotFound)
				return
			}
		}
		value, err := deps.Filings.Create(c.Request.Context(), filingActor(c), filing.CreateCommand{ProjectID: body.ProjectID, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, value)
	}
}

func getFiling(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		value, err := deps.Filings.Get(c.Request.Context(), filingActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func deleteFiling(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		value, err := deps.Filings.DeleteDraft(c.Request.Context(), filingActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func saveFilingSection(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		var body struct {
			ExpectedVersion uint64          `json:"expected_version"`
			Data            json.RawMessage `json:"data"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.Filings.SaveSection(c.Request.Context(), filingActor(c), c.Param("id"), c.Param("code"), filing.SaveSectionCommand{ExpectedVersion: body.ExpectedVersion, Data: body.Data, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func saveFilingMatrix(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		var body struct {
			ExpectedFilingVersion uint64 `json:"expected_filing_version"`
			ExpectedMatrixVersion uint64 `json:"expected_matrix_version"`
			RowCode               string `json:"row_code"`
			ColumnCode            string `json:"column_code"`
			Selected              bool   `json:"selected"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.Filings.SaveMatrix(c.Request.Context(), filingActor(c), c.Param("id"), c.Param("code"), filing.SaveMatrixCommand{ExpectedFilingVersion: body.ExpectedFilingVersion, ExpectedMatrixVersion: body.ExpectedMatrixVersion, RowCode: body.RowCode, ColumnCode: body.ColumnCode, Selected: body.Selected, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func validateFiling(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		value, err := deps.Filings.Validate(c.Request.Context(), filingActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func submitFiling(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		var body struct {
			ExpectedVersion uint64 `json:"expected_version"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.Filings.Submit(c.Request.Context(), filingActor(c), c.Param("id"), filing.SubmitCommand{ExpectedVersion: body.ExpectedVersion, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func createFilingMaterialUpload(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.FilingMaterials == nil {
			response.Error(c, filing.ErrMaterialUnavailable)
			return
		}
		var body struct {
			MaterialCode string `json:"material_code"`
			FileName     string `json:"file_name"`
			MIMEType     string `json:"mime_type"`
			SizeBytes    uint64 `json:"size_bytes"`
			SHA256       string `json:"sha256"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.FilingMaterials.CreateUpload(c.Request.Context(), filingActor(c), c.Param("id"), filing.MaterialUploadCommand{MaterialCode: body.MaterialCode, FileName: body.FileName, MIMEType: body.MIMEType, SizeBytes: body.SizeBytes, SHA256: body.SHA256, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, value)
	}
}

func completeFilingMaterialUpload(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.FilingMaterials == nil {
			response.Error(c, filing.ErrMaterialUnavailable)
			return
		}
		var body struct {
			ExpectedVersion uint64 `json:"expected_version"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.FilingMaterials.CompleteUpload(c.Request.Context(), filingActor(c), c.Param("id"), c.Param("materialID"), body.ExpectedVersion)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func unlockFiling(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, _ := sharedauth.FromContext(c.Request.Context())
		var body struct {
			CustomerID      uint64 `json:"customer_id"`
			ExpectedVersion uint64 `json:"expected_version"`
			Reason          string `json:"reason"`
		}
		if !decode(c, &body) {
			return
		}
		if deps.Filings == nil {
			response.Error(c, apperror.ErrNotFound)
			return
		}
		value, err := deps.Filings.Unlock(c.Request.Context(), filing.InternalActor{TenantID: principal.TenantID, ActorID: principal.UserID}, c.Param("id"), filing.UnlockCommand{CustomerID: body.CustomerID, ExpectedVersion: body.ExpectedVersion, Reason: body.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func filingMaterialScanCallback(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.FilingMaterials == nil {
			response.Error(c, filing.ErrMaterialUnavailable)
			return
		}
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || strings.TrimSpace(principal.TenantID) == "" {
			response.Error(c, apperror.ErrForbidden)
			return
		}
		var body struct {
			MaterialID    string    `json:"material_id"`
			ScanReference string    `json:"scan_reference"`
			Status        string    `json:"status"`
			OccurredAt    time.Time `json:"occurred_at"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.FilingMaterials.ApplyScan(c.Request.Context(), principal.TenantID, filing.MaterialScanEvent{MaterialID: body.MaterialID, ScanReference: body.ScanReference, Status: body.Status, OccurredAt: body.OccurredAt})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func listFeedback(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		query, ok := bindFeedbackListQuery(c)
		if !ok {
			return
		}
		value, err := deps.Feedback.List(c.Request.Context(), feedbackActor(c), query)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func getFeedback(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rejectFeedbackQuery(c) {
			return
		}
		value, err := deps.Feedback.Get(c.Request.Context(), feedbackActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func createFeedback(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rejectFeedbackQuery(c) {
			return
		}
		var body struct {
			Type            string `json:"type"`
			Title           string `json:"title"`
			Description     string `json:"description"`
			ProjectID       string `json:"project_id"`
			ExpectedContact string `json:"expected_contact"`
		}
		if !decode(c, &body) {
			return
		}
		if body.ProjectID != "" && !portalProjectVisible(c, deps, body.ProjectID) {
			return
		}
		value, err := deps.Feedback.Create(c.Request.Context(), feedbackActor(c), feedback.CreateCommand{Type: body.Type, Title: body.Title, Description: body.Description, ProjectID: body.ProjectID, ExpectedContact: body.ExpectedContact, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, value)
	}
}

func addFeedbackMessage(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rejectFeedbackQuery(c) {
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.Feedback.AddCustomerMessage(c.Request.Context(), feedbackActor(c), c.Param("id"), feedback.MessageCommand{Content: body.Content, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func closeFeedback(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rejectFeedbackQuery(c) {
			return
		}
		value, err := deps.Feedback.Close(c.Request.Context(), feedbackActor(c), c.Param("id"), feedback.CloseCommand{IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func evaluationActor(c *gin.Context) evaluation.Actor {
	session := currentSession(c)
	return evaluation.Actor{TenantID: session.TenantID, CustomerID: session.CustomerID, AccountID: session.PlatformUserID}
}

func evaluationEligibility(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !portalProjectVisible(c, deps, c.Param("projectID")) {
			return
		}
		value, err := deps.Evaluations.Eligibility(c.Request.Context(), evaluationActor(c), c.Param("projectID"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func submitEvaluation(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			ProjectID         string `json:"project_id"`
			ProfessionalScore uint8  `json:"professional_score"`
			ResponseScore     uint8  `json:"response_score"`
			ReportScore       uint8  `json:"report_score"`
			AttitudeScore     uint8  `json:"attitude_score"`
			Comment           string `json:"comment"`
		}
		if !decode(c, &body) {
			return
		}
		if !portalProjectVisible(c, deps, body.ProjectID) {
			return
		}
		value, err := deps.Evaluations.Submit(c.Request.Context(), evaluationActor(c), evaluation.SubmitCommand{
			ProjectID: body.ProjectID, ProfessionalScore: body.ProfessionalScore,
			ResponseScore: body.ResponseScore, ReportScore: body.ReportScore,
			AttitudeScore: body.AttitudeScore, Comment: body.Comment,
			IdempotencyKey: c.GetHeader("Idempotency-Key"),
		})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, value)
	}
}

func getEvaluation(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		value, err := deps.Evaluations.Get(c.Request.Context(), evaluationActor(c), c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func evaluationStatistics(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, _ := sharedauth.FromContext(c.Request.Context())
		value, err := deps.Evaluations.Statistics(c.Request.Context(), principal.TenantID)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func listEvaluationNotices(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, _ := sharedauth.FromContext(c.Request.Context())
		page, _ := strconv.Atoi(c.Query("page"))
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		value, err := deps.Evaluations.ListLowScoreNotices(c.Request.Context(), principal.TenantID, c.Query("status"), page, pageSize)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func readEvaluationNotice(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, _ := sharedauth.FromContext(c.Request.Context())
		value, err := deps.Evaluations.ReadLowScoreNotice(c.Request.Context(), principal.TenantID, principal.UserID, c.Param("id"))
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func listFeedbackForOperator(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, _ := sharedauth.FromContext(c.Request.Context())
		query, ok := bindFeedbackListQuery(c)
		if !ok {
			return
		}
		value, err := deps.Feedback.ListForOperator(c.Request.Context(), feedback.OperatorActor{TenantID: principal.TenantID, ActorID: principal.UserID}, query)
		if err != nil {
			response.Error(c, err)
			return
		}
		// 在独立的运营端应用契约获批前，机器接口仍返回与客户侧相同的最小 DTO，避免扩大数据披露。
		items := make([]feedback.CustomerFeedback, 0, len(value.Items))
		for i := range value.Items {
			items = append(items, feedbackCustomerView(&value.Items[i]))
		}
		response.OK(c, gin.H{"items": items, "page": value.Page, "page_size": value.PageSize, "total": value.Total})
	}
}

func feedbackCustomerView(value *feedback.Feedback) feedback.CustomerFeedback {
	return feedback.CustomerFeedback{ID: value.PublicID, FeedbackNo: value.FeedbackNo, ProjectID: value.ProjectID, Type: value.Type, Title: value.Title, Description: value.Description, ExpectedContactMasked: value.ExpectedContactMasked, Status: value.Status, RejectReason: value.RejectReason, SubmittedAt: value.SubmittedAt, FirstResponseDueAt: value.FirstResponseDueAt, FirstRespondedAt: value.FirstRespondedAt, ResolvedAt: value.ResolvedAt, ClosedAt: value.ClosedAt}
}

func processFeedback(deps RouterDependencies, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, _ := sharedauth.FromContext(c.Request.Context())
		if !rejectFeedbackQuery(c) {
			return
		}
		var body struct {
			Content string `json:"content"`
			Reason  string `json:"reason"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.Feedback.Process(c.Request.Context(), feedback.OperatorActor{TenantID: principal.TenantID, ActorID: principal.UserID}, c.Param("id"), action, feedback.ProcessCommand{Content: body.Content, Reason: body.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

// 客户侧与运营侧列表查询保持显式契约；非法分页直接拒绝而非静默归一化，避免客户端实际执行
// 与其意图不同的查询。
func bindFeedbackListQuery(c *gin.Context) (feedback.ListQuery, bool) {
	page, pageSize, ok := bindProjectPagination(c, "status", "type")
	if !ok {
		return feedback.ListQuery{}, false
	}
	return feedback.ListQuery{
		Status: c.Query("status"),
		Type:   c.Query("type"),
		Page:   page, PageSize: pageSize,
	}, true
}

// 详情和变更命令不接受查询参数控制行为；在此处理器校验前，认证、权限/范围及 Origin/CSRF
// 中间件已经执行。
func rejectFeedbackQuery(c *gin.Context) bool {
	if onlyProjectQueryKeys(c) {
		return true
	}
	response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
	return false
}
