package bootstrap

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

const (
	capabilityModeReady        = "READY"
	capabilityModeFailClosed   = "FAIL_CLOSED"
	capabilityModeCallbackOnly = "CALLBACK_ONLY"
)

// RuntimeCapability 只描述当前进程是否装配了生产适配器。它既不是授权结论，也不是远端
// 健康探测；每个业务接口执行时仍需独立校验权限和依赖状态。
type RuntimeCapability struct {
	Available  bool   `json:"available"`
	Mode       string `json:"mode"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type RuntimeCapabilities map[string]RuntimeCapability

func configuredCapability(available bool, unavailableReason string) RuntimeCapability {
	if available {
		return RuntimeCapability{Available: true, Mode: capabilityModeReady}
	}
	return RuntimeCapability{Available: false, Mode: capabilityModeFailClosed, ReasonCode: unavailableReason}
}

func runtimeCapabilities(config Config) RuntimeCapabilities {
	portalReady := config.PortalInviteEnabled && config.PlatformExternalIdentityEnabled
	qbStatus := configuredCapability(config.QBStatusQueryEnabled, "QB_ACTIVE_QUERY_NOT_CONFIGURED")
	if !config.QBStatusQueryEnabled {
		qbStatus.Mode = capabilityModeCallbackOnly
	}
	return RuntimeCapabilities{
		"owner_directory":          configuredCapability(config.OwnerDirectoryEnabled, "OWNER_DIRECTORY_NOT_CONFIGURED"),
		"portal_account_provision": configuredCapability(portalReady, "PORTAL_IDENTITY_PROVIDER_NOT_CONFIGURED"),
		"portal_access_disable":    configuredCapability(portalReady, "PORTAL_IDENTITY_PROVIDER_NOT_CONFIGURED"),
		"approval_task_query":      configuredCapability(config.ApprovalTaskResolverEnabled, "APPROVAL_TASK_RESOLVER_NOT_CONFIGURED"),
		"qb_active_query":          qbStatus,
		"qb_launch_quotation":      configuredCapability(config.QBLaunchEnabled, "QB_LAUNCH_NOT_CONFIGURED"),
		"qb_launch_bid":            configuredCapability(config.QBLaunchEnabled, "QB_LAUNCH_NOT_CONFIGURED"),
		// 导入和附件使用进程内有界解析、格式、摘要、路径和内容校验，不依赖外部杀毒服务。
		"customer_import_scan":            configuredCapability(true, ""),
		"opportunity_attachment_upload":   configuredCapability(config.AttachmentLocalEnabled, "ATTACHMENT_STORAGE_OR_SCANNER_NOT_CONFIGURED"),
		"opportunity_attachment_download": configuredCapability(config.AttachmentLocalEnabled, "ATTACHMENT_STORAGE_NOT_CONFIGURED"),
		"customer_project_history":        configuredCapability(config.PortalProjectHistoryEnabled, "PROJECT_HISTORY_PROVIDER_NOT_CONFIGURED"),
		"customer_export":                 configuredCapability(true, ""),
		"presale_report_export":           configuredCapability(false, "PRESALE_EXPORT_PROVIDER_NOT_CONFIGURED"),
		"presale_request_submission":      configuredCapability(true, ""),
	}
}

func resolveRuntimeCapabilities(ctx context.Context, config Config, readiness presale.WorkerReadiness, maxAge time.Duration, now time.Time) RuntimeCapabilities {
	return runtimeCapabilities(config)
}

func runtimeCapabilitiesHandler(config Config, readiness presale.WorkerReadiness, maxAge time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		capabilities := resolveRuntimeCapabilities(c.Request.Context(), config, readiness, maxAge, time.Now())
		response.OK(c, gin.H{"capabilities": capabilities})
	}
}
