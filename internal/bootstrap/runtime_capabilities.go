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

// RuntimeCapability describes only whether this process has a production
// adapter configured. It is neither authorization nor a remote health probe;
// every business endpoint still performs its own permission and dependency
// checks when an operation is executed.
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
		// The current bootstrap intentionally injects unavailable trust-boundary
		// adapters. Keep these values explicit until production implementations
		// are wired into this process.
		"customer_import_scan":            configuredCapability(false, "CUSTOMER_IMPORT_SCANNER_NOT_CONFIGURED"),
		"opportunity_attachment_upload":   configuredCapability(false, "ATTACHMENT_STORAGE_OR_SCANNER_NOT_CONFIGURED"),
		"opportunity_attachment_download": configuredCapability(false, "ATTACHMENT_STORAGE_NOT_CONFIGURED"),
		"customer_project_history":        configuredCapability(config.PortalProjectHistoryEnabled, "PROJECT_HISTORY_PROVIDER_NOT_CONFIGURED"),
		"customer_export":                 configuredCapability(false, "CUSTOMER_EXPORT_PROVIDER_NOT_CONFIGURED"),
		"presale_report_export":           configuredCapability(false, "PRESALE_EXPORT_PROVIDER_NOT_CONFIGURED"),
		"presale_request_submission":      configuredCapability(false, "PRESALE_DELIVERY_WORKER_UNAVAILABLE"),
	}
}

func resolveRuntimeCapabilities(ctx context.Context, config Config, readiness presale.WorkerReadiness, maxAge time.Duration, now time.Time) RuntimeCapabilities {
	capabilities := runtimeCapabilities(config)
	if readiness == nil || maxAge <= 0 {
		return capabilities
	}
	available, err := readiness.HasFreshHeartbeat(ctx, presale.PresaleDeliveryWorkerType, now.UTC().Add(-maxAge))
	if err == nil && available {
		capabilities["presale_request_submission"] = configuredCapability(true, "")
	}
	return capabilities
}

func runtimeCapabilitiesHandler(config Config, readiness presale.WorkerReadiness, maxAge time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		capabilities := resolveRuntimeCapabilities(c.Request.Context(), config, readiness, maxAge, time.Now())
		response.OK(c, gin.H{"capabilities": capabilities})
	}
}
