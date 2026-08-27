package bootstrap

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
)

// 本地会话最长只缓存授权十五分钟；实际过期时间还会受平台令牌有效期和服务端重验约束。
const maxOIDCSessionTTL = 15 * time.Minute

type Config struct {
	Address                   string
	MySQLDSN                  string
	PathPrefix                string
	PublicOrigin              string
	EncryptionKey             []byte
	HMACKey                   []byte
	OIDCIssuer                string
	OIDCBackchannelBaseURL    string
	OIDCClientID              string
	OIDCClientSecret          string
	OIDCIDPHint               string
	OIDCRedirectURI           string
	OIDCPostLogoutRedirectURI string
	OIDCScopes                []string
	OIDCTenantID              string
	OIDCRoleConfigHash        string
	OIDCSessionCookieName     string
	OIDCSessionTTL            time.Duration
	OIDCSessionSecure         bool
	OIDCMaxRoles              int
	// AllowInsecureHTTPSession 仅用于无 HTTPS 的测试服务器：显式开启后允许非回环 HTTP
	// 源使用非 Secure 会话 Cookie。默认关闭，生产部署不得开启。
	AllowInsecureHTTPSession         bool
	MachineTokenIssuer               string
	MachineTokenAudience             string
	MachineTokenPublicKeyPath        string
	PortalInviteEnabled              bool
	PlatformExternalIdentityEnabled  bool
	PlatformExternalUserProvisionURL string
	PlatformApplicationRoleAssignURL string
	PlatformApplicationRoleRevokeURL string
	PlatformManagementTokenURL       string
	PlatformPortalApplicationCode    string
	PlatformExternalUserClientID     string
	PlatformExternalUserClientSecret string
	PlatformExternalUserScope        string
	PlatformRoleAssignClientID       string
	PlatformRoleAssignClientSecret   string
	PlatformRoleAssignScope          string
	PlatformRoleRevokeClientID       string
	PlatformRoleRevokeClientSecret   string
	PlatformRoleRevokeScope          string
	PlatformCustomerBindingBaseURL   string
	PortalMappingDualWrite           bool
	PortalMappingPlatformOnly        bool
	OwnerDirectoryEnabled            bool
	PlatformOwnerDirectoryURL        string
	PlatformOwnerDirectoryClientID   string
	PlatformOwnerDirectorySecret     string
	PlatformOwnerDirectoryScope      string
	PlatformManagementTLS            integrationhttp.TLSOptions
	ApprovalTaskResolverEnabled      bool
	ApprovalTaskURL                  string
	ApprovalTaskTokenURL             string
	ApprovalTaskClientID             string
	ApprovalTaskClientSecret         string
	ApprovalTaskScope                string
	ApprovalTaskTLS                  integrationhttp.TLSOptions
	PortalPublicURL                  string
	PortalProvisionURL               string
	PortalProvisionTokenURL          string
	PortalProvisionClientID          string
	PortalProvisionClientSecret      string
	PortalProvisionScope             string
	PortalDisableURL                 string
	PortalDisableClientID            string
	PortalDisableClientSecret        string
	PortalDisableScope               string
	PortalProvisionTLS               integrationhttp.TLSOptions
	PortalInvitePepper               []byte
	PortalProjectHistoryEnabled      bool
	PortalProjectHistoryURL          string
	PortalProjectHistoryTokenURL     string
	PortalProjectHistoryClientID     string
	PortalProjectHistoryClientSecret string
	PortalProjectHistoryScope        string
	ContractVerificationEnabled      bool
	ContractSummaryURL               string
	ContractSummaryTokenURL          string
	ContractSummaryClientID          string
	ContractSummaryClientSecret      string
	ContractSummaryScope             string
	ContractSignedCountEnabled       bool
	ContractSignedCountURL           string
	ContractSignedCountTokenURL      string
	ContractSignedCountClientID      string
	ContractSignedCountClientSecret  string
	ContractSignedCountScope         string
	QBStatusQueryEnabled             bool
	QBStatusURL                      string
	QBStatusTokenURL                 string
	QBStatusClientID                 string
	QBStatusClientSecret             string
	QBStatusScope                    string
	QBStatusTLS                      integrationhttp.TLSOptions
	QBLaunchEnabled                  bool
	QBQuotationPublicURL             string
	QBBidPublicURL                   string
	QBLaunchSigningKey               []byte
	QBLaunchTTL                      time.Duration
	AttachmentLocalEnabled           bool
	AttachmentLocalRoot              string
	AttachmentS3Enabled              bool
	AttachmentS3Endpoint             string
	AttachmentS3Region               string
	AttachmentS3Bucket               string
	AttachmentS3AccessKeyID          string
	AttachmentS3SecretAccessKey      string
	AttachmentS3PathStyle            bool
	AttachmentS3Prefix               string
	ClamAVEnabled                    bool
	ClamAVNetwork                    string
	ClamAVAddress                    string
	PlatformBaseURL                  string
	PlatformApplicationCode          string
	PlatformEnvironmentCode          string
	PlatformAuditClientID            string
	PlatformAuditClientSecret        string
	PlatformAuditWorkerID            string
	PlatformAuditPollInterval        time.Duration
	PlatformAuditBatchSize           int
	CatalogSyncEnabled               bool
	CatalogApplicationID             string
	CatalogClientID                  string
	CatalogClientSecret              string
	PresaleWorkerHeartbeatMaxAge     time.Duration
}

func LoadConfig() (Config, error) {
	// 配置在进程开放监听端口前一次性解析并校验。任何布尔值、时长或密钥格式错误都失败退出，
	// 避免运行中根据环境变量变化形成同一副本内不一致的安全策略。
	developmentAuth, err := strconv.ParseBool(valueOrDefault("DEV_AUTH_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("DEV_AUTH_ENABLED: %w", err)
	}
	if developmentAuth {
		return Config{}, fmt.Errorf("DEV_AUTH_ENABLED is no longer supported; CRM identity and authorization must use the base platform")
	}
	encryptionKey, err := base64.StdEncoding.DecodeString(os.Getenv("SENSITIVE_ENCRYPTION_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("SENSITIVE_ENCRYPTION_KEY_BASE64: %w", err)
	}
	hmacKey, err := base64.StdEncoding.DecodeString(os.Getenv("SENSITIVE_HMAC_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("SENSITIVE_HMAC_KEY_BASE64: %w", err)
	}
	invitePepper, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_INVITE_PEPPER_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_INVITE_PEPPER_BASE64: %w", err)
	}
	// 邀请摘要建议使用独立轮换的 pepper；为兼容既有部署，可回退到已有秘密 HMAC 材料，
	// 但绝不接受空值或公开常量。
	if len(invitePepper) == 0 {
		invitePepper = append([]byte(nil), hmacKey...)
	}
	sessionSecure, err := strconv.ParseBool(valueOrDefault("OIDC_SESSION_COOKIE_SECURE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("OIDC_SESSION_COOKIE_SECURE: %w", err)
	}
	allowInsecureHTTPSession, err := strconv.ParseBool(valueOrDefault("OIDC_ALLOW_INSECURE_HTTP_SESSION", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("OIDC_ALLOW_INSECURE_HTTP_SESSION: %w", err)
	}
	sessionTTL, err := time.ParseDuration(valueOrDefault("OIDC_SESSION_TTL", "15m"))
	if err != nil {
		return Config{}, fmt.Errorf("OIDC_SESSION_TTL: %w", err)
	}
	maxRoles, err := strconv.Atoi(valueOrDefault("OIDC_MAX_EFFECTIVE_ROLES", "10"))
	if err != nil {
		return Config{}, fmt.Errorf("OIDC_MAX_EFFECTIVE_ROLES: %w", err)
	}
	portalInviteEnabled, err := strconv.ParseBool(valueOrDefault("PORTAL_INVITE_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_INVITE_ENABLED: %w", err)
	}
	platformExternalIdentityEnabled, err := strconv.ParseBool(valueOrDefault("PLATFORM_EXTERNAL_IDENTITY_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_EXTERNAL_IDENTITY_ENABLED: %w", err)
	}
	portalMappingDualWrite, err := strconv.ParseBool(valueOrDefault("PORTAL_MAPPING_DUAL_WRITE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_MAPPING_DUAL_WRITE: %w", err)
	}
	portalMappingPlatformOnly, err := strconv.ParseBool(valueOrDefault("PORTAL_MAPPING_PLATFORM_ONLY", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_MAPPING_PLATFORM_ONLY: %w", err)
	}
	ownerDirectoryEnabled, err := strconv.ParseBool(valueOrDefault("OWNER_DIRECTORY_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("OWNER_DIRECTORY_ENABLED: %w", err)
	}
	approvalTaskResolverEnabled, err := strconv.ParseBool(valueOrDefault("APPROVAL_TASK_RESOLVER_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("APPROVAL_TASK_RESOLVER_ENABLED: %w", err)
	}
	platformManagementRequireMTLS, err := strconv.ParseBool(valueOrDefault("PLATFORM_MANAGEMENT_TLS_REQUIRE_MTLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_MANAGEMENT_TLS_REQUIRE_MTLS: %w", err)
	}
	portalProvisionRequireMTLS, err := strconv.ParseBool(valueOrDefault("PORTAL_PROVISION_TLS_REQUIRE_MTLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_PROVISION_TLS_REQUIRE_MTLS: %w", err)
	}
	approvalTaskRequireMTLS, err := strconv.ParseBool(valueOrDefault("APPROVAL_TASK_TLS_REQUIRE_MTLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("APPROVAL_TASK_TLS_REQUIRE_MTLS: %w", err)
	}
	portalProjectHistoryEnabled, err := strconv.ParseBool(valueOrDefault("PORTAL_PROJECT_HISTORY_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_PROJECT_HISTORY_ENABLED: %w", err)
	}
	contractVerificationEnabled, err := strconv.ParseBool(valueOrDefault("CONTRACT_VERIFICATION_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("CONTRACT_VERIFICATION_ENABLED: %w", err)
	}
	contractSignedCountEnabled, err := strconv.ParseBool(valueOrDefault("CONTRACT_SIGNED_COUNT_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("CONTRACT_SIGNED_COUNT_ENABLED: %w", err)
	}
	qbStatusQueryEnabled, err := strconv.ParseBool(valueOrDefault("QB_STATUS_QUERY_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("QB_STATUS_QUERY_ENABLED: %w", err)
	}
	qbRequireMTLS, err := strconv.ParseBool(valueOrDefault("QB_STATUS_TLS_REQUIRE_MTLS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("QB_STATUS_TLS_REQUIRE_MTLS: %w", err)
	}
	qbLaunchEnabled, err := strconv.ParseBool(valueOrDefault("QB_LAUNCH_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("QB_LAUNCH_ENABLED: %w", err)
	}
	qbLaunchTTL, err := time.ParseDuration(valueOrDefault("QB_LAUNCH_TTL", "2m"))
	if err != nil {
		return Config{}, fmt.Errorf("QB_LAUNCH_TTL: %w", err)
	}
	qbLaunchSigningKey, err := base64.StdEncoding.DecodeString(os.Getenv("QB_LAUNCH_SIGNING_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("QB_LAUNCH_SIGNING_KEY_BASE64: %w", err)
	}
	presaleWorkerHeartbeatMaxAge, err := time.ParseDuration(valueOrDefault("PRESALE_WORKER_HEARTBEAT_MAX_AGE", "15s"))
	if err != nil {
		return Config{}, fmt.Errorf("PRESALE_WORKER_HEARTBEAT_MAX_AGE: %w", err)
	}
	platformAuditPollInterval, err := time.ParseDuration(valueOrDefault("PLATFORM_AUDIT_POLL_INTERVAL", "1s"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_AUDIT_POLL_INTERVAL: %w", err)
	}
	platformAuditBatchSize, err := strconv.Atoi(valueOrDefault("PLATFORM_AUDIT_BATCH_SIZE", "100"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_AUDIT_BATCH_SIZE: %w", err)
	}
	catalogSyncEnabled, err := strconv.ParseBool(valueOrDefault("PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED: %w", err)
	}
	attachmentLocalEnabled, err := strconv.ParseBool(valueOrDefault("ATTACHMENT_LOCAL_ENABLED", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("ATTACHMENT_LOCAL_ENABLED: %w", err)
	}
	attachmentS3Enabled, err := strconv.ParseBool(valueOrDefault("ATTACHMENT_S3_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("ATTACHMENT_S3_ENABLED: %w", err)
	}
	attachmentS3PathStyle, err := strconv.ParseBool(valueOrDefault("ATTACHMENT_S3_PATH_STYLE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("ATTACHMENT_S3_PATH_STYLE: %w", err)
	}
	if attachmentS3Enabled {
		// S3 适配器启用时要求完整的连接参数；缺少任何一项都在启动期失败，
		// 避免运行中在本地降级与对象存储之间静默切换信任边界。
		if strings.TrimSpace(os.Getenv("ATTACHMENT_S3_ENDPOINT")) == "" || strings.TrimSpace(os.Getenv("ATTACHMENT_S3_REGION")) == "" ||
			strings.TrimSpace(os.Getenv("ATTACHMENT_S3_BUCKET")) == "" || strings.TrimSpace(os.Getenv("ATTACHMENT_S3_ACCESS_KEY_ID")) == "" ||
			strings.TrimSpace(os.Getenv("ATTACHMENT_S3_SECRET_ACCESS_KEY")) == "" {
			return Config{}, fmt.Errorf("ATTACHMENT_S3_ENABLED requires ATTACHMENT_S3_ENDPOINT, ATTACHMENT_S3_REGION, ATTACHMENT_S3_BUCKET, ATTACHMENT_S3_ACCESS_KEY_ID and ATTACHMENT_S3_SECRET_ACCESS_KEY")
		}
	}
	clamAVEnabled, err := strconv.ParseBool(valueOrDefault("CLAMAV_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("CLAMAV_ENABLED: %w", err)
	}
	clamAVNetwork := valueOrDefault("CLAMAV_NETWORK", "tcp")
	if clamAVEnabled {
		if clamAVNetwork != "tcp" && clamAVNetwork != "unix" {
			return Config{}, fmt.Errorf("CLAMAV_NETWORK must be tcp or unix")
		}
		if strings.TrimSpace(os.Getenv("CLAMAV_ADDRESS")) == "" {
			return Config{}, fmt.Errorf("CLAMAV_ENABLED requires CLAMAV_ADDRESS")
		}
	}
	config := Config{
		Address: valueOrDefault("HTTP_ADDRESS", ":8090"), MySQLDSN: os.Getenv("MYSQL_DSN"), PathPrefix: valueOrDefault("APP_PATH_PREFIX", "/customer-opportunity"), PublicOrigin: os.Getenv("APP_PUBLIC_ORIGIN"), EncryptionKey: encryptionKey, HMACKey: hmacKey,
		OIDCIssuer: os.Getenv("OIDC_ISSUER"), OIDCBackchannelBaseURL: os.Getenv("OIDC_BACKCHANNEL_BASE_URL"),
		OIDCClientID: os.Getenv("OIDC_CLIENT_ID"), OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), OIDCIDPHint: strings.TrimSpace(valueOrDefault("OIDC_IDP_HINT", "basic-platform")), OIDCRedirectURI: os.Getenv("OIDC_REDIRECT_URI"), OIDCPostLogoutRedirectURI: os.Getenv("OIDC_POST_LOGOUT_REDIRECT_URI"),
		OIDCScopes: splitFields(valueOrDefault("OIDC_SCOPES", "openid profile")), OIDCTenantID: os.Getenv("OIDC_TENANT_ID"), OIDCRoleConfigHash: os.Getenv("OIDC_ROLE_CONFIG_HASH"),
		OIDCSessionCookieName: valueOrDefault("OIDC_SESSION_COOKIE_NAME", "customer_opportunity_session"), OIDCSessionTTL: sessionTTL, OIDCSessionSecure: sessionSecure, OIDCMaxRoles: maxRoles,
		AllowInsecureHTTPSession: allowInsecureHTTPSession,
		MachineTokenIssuer:       os.Getenv("MACHINE_TOKEN_ISSUER"), MachineTokenAudience: os.Getenv("MACHINE_TOKEN_AUDIENCE"),
		MachineTokenPublicKeyPath:        os.Getenv("MACHINE_TOKEN_PUBLIC_KEY_PATH"),
		PortalInviteEnabled:              portalInviteEnabled,
		PlatformExternalIdentityEnabled:  platformExternalIdentityEnabled,
		PlatformExternalUserProvisionURL: os.Getenv("PLATFORM_EXTERNAL_USER_PROVISION_URL"),
		PlatformApplicationRoleAssignURL: os.Getenv("PLATFORM_APPLICATION_ROLE_ASSIGN_URL"),
		PlatformApplicationRoleRevokeURL: os.Getenv("PLATFORM_APPLICATION_ROLE_REVOKE_URL"),
		PlatformManagementTokenURL:       os.Getenv("PLATFORM_MANAGEMENT_TOKEN_URL"),
		PlatformPortalApplicationCode:    os.Getenv("PLATFORM_PORTAL_APPLICATION_CODE"),
		PlatformExternalUserClientID:     os.Getenv("PLATFORM_EXTERNAL_USER_CLIENT_ID"),
		PlatformExternalUserClientSecret: os.Getenv("PLATFORM_EXTERNAL_USER_CLIENT_SECRET"),
		PlatformExternalUserScope:        valueOrDefault("PLATFORM_EXTERNAL_USER_SCOPE", "external_user.provision"),
		PlatformRoleAssignClientID:       os.Getenv("PLATFORM_ROLE_ASSIGN_CLIENT_ID"),
		PlatformRoleAssignClientSecret:   os.Getenv("PLATFORM_ROLE_ASSIGN_CLIENT_SECRET"),
		PlatformCustomerBindingBaseURL:   os.Getenv("PLATFORM_CUSTOMER_BINDING_BASE_URL"),
		PortalMappingDualWrite:           portalMappingDualWrite,
		PortalMappingPlatformOnly:        portalMappingPlatformOnly,
		PlatformRoleAssignScope:          valueOrDefault("PLATFORM_ROLE_ASSIGN_SCOPE", "application_role.assign"),
		PlatformRoleRevokeClientID:       os.Getenv("PLATFORM_ROLE_REVOKE_CLIENT_ID"),
		PlatformRoleRevokeClientSecret:   os.Getenv("PLATFORM_ROLE_REVOKE_CLIENT_SECRET"),
		PlatformRoleRevokeScope:          valueOrDefault("PLATFORM_ROLE_REVOKE_SCOPE", "application_role.revoke"),
		OwnerDirectoryEnabled:            ownerDirectoryEnabled,
		PlatformOwnerDirectoryURL:        os.Getenv("PLATFORM_OWNER_DIRECTORY_URL"),
		PlatformOwnerDirectoryClientID:   os.Getenv("PLATFORM_OWNER_DIRECTORY_CLIENT_ID"),
		PlatformOwnerDirectorySecret:     os.Getenv("PLATFORM_OWNER_DIRECTORY_CLIENT_SECRET"),
		PlatformOwnerDirectoryScope:      valueOrDefault("PLATFORM_OWNER_DIRECTORY_SCOPE", "owner_directory.read"),
		PlatformManagementTLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("PLATFORM_MANAGEMENT_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PLATFORM_MANAGEMENT_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("PLATFORM_MANAGEMENT_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PLATFORM_MANAGEMENT_TLS_SERVER_NAME"),
			RequireMTLS: platformManagementRequireMTLS,
		},
		ApprovalTaskResolverEnabled: approvalTaskResolverEnabled,
		ApprovalTaskURL:             os.Getenv("APPROVAL_TASK_URL"), ApprovalTaskTokenURL: os.Getenv("APPROVAL_TASK_TOKEN_URL"),
		ApprovalTaskClientID: os.Getenv("APPROVAL_TASK_CLIENT_ID"), ApprovalTaskClientSecret: os.Getenv("APPROVAL_TASK_CLIENT_SECRET"),
		ApprovalTaskScope: valueOrDefault("APPROVAL_TASK_SCOPE", "presale.approval.task.read"),
		ApprovalTaskTLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("APPROVAL_TASK_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("APPROVAL_TASK_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("APPROVAL_TASK_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("APPROVAL_TASK_TLS_SERVER_NAME"),
			RequireMTLS: approvalTaskRequireMTLS,
		},
		PortalPublicURL: valueOrDefault("PORTAL_PUBLIC_URL", "http://localhost:8091/customer-portal"), PortalProvisionURL: os.Getenv("PORTAL_PROVISION_URL"),
		PortalProvisionTokenURL: os.Getenv("PORTAL_PROVISION_TOKEN_URL"), PortalProvisionClientID: os.Getenv("PORTAL_PROVISION_CLIENT_ID"), PortalProvisionClientSecret: os.Getenv("PORTAL_PROVISION_CLIENT_SECRET"),
		PortalProvisionScope: valueOrDefault("PORTAL_PROVISION_SCOPE", "portal.identity_mapping.provision"),
		PortalDisableURL:     os.Getenv("PORTAL_DISABLE_URL"), PortalDisableClientID: os.Getenv("PORTAL_DISABLE_CLIENT_ID"),
		PortalDisableClientSecret: os.Getenv("PORTAL_DISABLE_CLIENT_SECRET"), PortalDisableScope: valueOrDefault("PORTAL_DISABLE_SCOPE", "portal.identity_mapping.disable"),
		PortalProvisionTLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("PORTAL_PROVISION_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("PORTAL_PROVISION_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("PORTAL_PROVISION_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("PORTAL_PROVISION_TLS_SERVER_NAME"), RequireMTLS: portalProvisionRequireMTLS,
		},
		PortalInvitePepper:          invitePepper,
		PortalProjectHistoryEnabled: portalProjectHistoryEnabled,
		PortalProjectHistoryURL:     os.Getenv("PORTAL_PROJECT_HISTORY_URL"), PortalProjectHistoryTokenURL: os.Getenv("PORTAL_PROJECT_HISTORY_TOKEN_URL"),
		PortalProjectHistoryClientID: os.Getenv("PORTAL_PROJECT_HISTORY_CLIENT_ID"), PortalProjectHistoryClientSecret: os.Getenv("PORTAL_PROJECT_HISTORY_CLIENT_SECRET"),
		PortalProjectHistoryScope:   valueOrDefault("PORTAL_PROJECT_HISTORY_SCOPE", "portal.project_history.read"),
		ContractVerificationEnabled: contractVerificationEnabled,
		ContractSummaryURL:          os.Getenv("CONTRACT_SUMMARY_URL"), ContractSummaryTokenURL: os.Getenv("CONTRACT_SUMMARY_TOKEN_URL"),
		ContractSummaryClientID: os.Getenv("CONTRACT_SUMMARY_CLIENT_ID"), ContractSummaryClientSecret: os.Getenv("CONTRACT_SUMMARY_CLIENT_SECRET"),
		ContractSummaryScope:            valueOrDefault("CONTRACT_SUMMARY_SCOPE", "contract.summary.read"),
		ContractSignedCountEnabled:      contractSignedCountEnabled,
		ContractSignedCountURL:          os.Getenv("CONTRACT_SIGNED_COUNT_URL"),
		ContractSignedCountTokenURL:     os.Getenv("CONTRACT_SIGNED_COUNT_TOKEN_URL"),
		ContractSignedCountClientID:     os.Getenv("CONTRACT_SIGNED_COUNT_CLIENT_ID"),
		ContractSignedCountClientSecret: os.Getenv("CONTRACT_SIGNED_COUNT_CLIENT_SECRET"),
		ContractSignedCountScope:        valueOrDefault("CONTRACT_SIGNED_COUNT_SCOPE", "contract.opportunity_signed_count.read"),
		QBStatusQueryEnabled:            qbStatusQueryEnabled,
		QBStatusURL:                     os.Getenv("QB_STATUS_URL"), QBStatusTokenURL: os.Getenv("QB_STATUS_TOKEN_URL"),
		QBStatusClientID: os.Getenv("QB_STATUS_CLIENT_ID"), QBStatusClientSecret: os.Getenv("QB_STATUS_CLIENT_SECRET"),
		QBStatusScope: valueOrDefault("QB_STATUS_SCOPE", "opportunity.status.read"),
		QBStatusTLS: integrationhttp.TLSOptions{
			RootCAFile: os.Getenv("QB_STATUS_TLS_ROOT_CA_FILE"), ClientCertFile: os.Getenv("QB_STATUS_TLS_CLIENT_CERT_FILE"),
			ClientKeyFile: os.Getenv("QB_STATUS_TLS_CLIENT_KEY_FILE"), ServerName: os.Getenv("QB_STATUS_TLS_SERVER_NAME"), RequireMTLS: qbRequireMTLS,
		},
		QBLaunchEnabled: qbLaunchEnabled, QBQuotationPublicURL: os.Getenv("QB_QUOTATION_PUBLIC_URL"),
		QBBidPublicURL: os.Getenv("QB_BID_PUBLIC_URL"), QBLaunchSigningKey: qbLaunchSigningKey, QBLaunchTTL: qbLaunchTTL,
		AttachmentLocalEnabled:    attachmentLocalEnabled,
		AttachmentLocalRoot:       valueOrDefault("ATTACHMENT_LOCAL_ROOT", "/app/data/attachments"),
		AttachmentS3Enabled:       attachmentS3Enabled,
		AttachmentS3Endpoint:      strings.TrimSpace(os.Getenv("ATTACHMENT_S3_ENDPOINT")),
		AttachmentS3Region:        strings.TrimSpace(os.Getenv("ATTACHMENT_S3_REGION")),
		AttachmentS3Bucket:        strings.TrimSpace(os.Getenv("ATTACHMENT_S3_BUCKET")),
		AttachmentS3AccessKeyID:   strings.TrimSpace(os.Getenv("ATTACHMENT_S3_ACCESS_KEY_ID")),
		AttachmentS3SecretAccessKey: strings.TrimSpace(os.Getenv("ATTACHMENT_S3_SECRET_ACCESS_KEY")),
		AttachmentS3PathStyle:     attachmentS3PathStyle,
		AttachmentS3Prefix:        strings.TrimSpace(os.Getenv("ATTACHMENT_S3_PREFIX")),
		ClamAVEnabled:             clamAVEnabled,
		ClamAVNetwork:             clamAVNetwork,
		ClamAVAddress:             strings.TrimSpace(os.Getenv("CLAMAV_ADDRESS")),
		PlatformBaseURL:           os.Getenv("PLATFORM_BASE_URL"),
		PlatformApplicationCode:   valueOrDefault("PLATFORM_APPLICATION_CODE", "customer_and_opportunity"),
		PlatformEnvironmentCode:   valueOrDefault("PLATFORM_ENVIRONMENT_CODE", "dev"),
		PlatformAuditClientID:     os.Getenv("PLATFORM_AUDIT_CLIENT_ID"),
		PlatformAuditClientSecret: os.Getenv("PLATFORM_AUDIT_CLIENT_SECRET"),
		PlatformAuditWorkerID:     valueOrDefault("PLATFORM_AUDIT_WORKER_ID", "crm-api-audit"),
		PlatformAuditPollInterval: platformAuditPollInterval,
		PlatformAuditBatchSize:    platformAuditBatchSize,
		CatalogSyncEnabled:        catalogSyncEnabled,
		CatalogApplicationID:      os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_APPLICATION_ID"), CatalogClientID: os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_ID"),
		CatalogClientSecret:          os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET"),
		PresaleWorkerHeartbeatMaxAge: presaleWorkerHeartbeatMaxAge,
	}
	if config.MySQLDSN == "" {
		return Config{}, fmt.Errorf("MYSQL_DSN is required")
	}
	if len(config.EncryptionKey) != 32 || len(config.HMACKey) < 32 {
		return Config{}, fmt.Errorf("sensitive encryption key must decode to 32 bytes and HMAC key to at least 32 bytes")
	}
	if config.PresaleWorkerHeartbeatMaxAge < 5*time.Second || config.PresaleWorkerHeartbeatMaxAge > 5*time.Minute {
		return Config{}, fmt.Errorf("PRESALE_WORKER_HEARTBEAT_MAX_AGE must be between 5s and 5m")
	}
	for key, value := range map[string]string{
		"PLATFORM_BASE_URL":            config.PlatformBaseURL,
		"PLATFORM_AUDIT_CLIENT_ID":     config.PlatformAuditClientID,
		"PLATFORM_AUDIT_CLIENT_SECRET": config.PlatformAuditClientSecret,
		"PLATFORM_APPLICATION_CODE":    config.PlatformApplicationCode,
		"PLATFORM_ENVIRONMENT_CODE":    config.PlatformEnvironmentCode,
		"PLATFORM_AUDIT_WORKER_ID":     config.PlatformAuditWorkerID,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return Config{}, fmt.Errorf("%s is required and must be trimmed", key)
		}
	}
	if config.PlatformApplicationCode != "customer_and_opportunity" {
		return Config{}, fmt.Errorf("PLATFORM_APPLICATION_CODE must be customer_and_opportunity")
	}
	if err := validateHTTPOrigin("PLATFORM_BASE_URL", config.PlatformBaseURL); err != nil {
		return Config{}, err
	}
	if config.PlatformAuditPollInterval < 100*time.Millisecond || config.PlatformAuditPollInterval > time.Minute {
		return Config{}, fmt.Errorf("PLATFORM_AUDIT_POLL_INTERVAL must be between 100ms and 1m")
	}
	if config.PlatformAuditBatchSize < 1 || config.PlatformAuditBatchSize > 100 {
		return Config{}, fmt.Errorf("PLATFORM_AUDIT_BATCH_SIZE must be between 1 and 100")
	}
	if config.PortalInviteEnabled && len(config.PortalInvitePepper) < 32 {
		return Config{}, fmt.Errorf("PORTAL_INVITE_PEPPER_BASE64 must decode to at least 32 bytes")
	}
	if config.PortalInviteEnabled {
		if config.PortalProvisionScope != "portal.identity_mapping.provision" {
			return Config{}, fmt.Errorf("PORTAL_PROVISION_SCOPE must be portal.identity_mapping.provision")
		}
		if config.PortalDisableScope != "portal.identity_mapping.disable" {
			return Config{}, fmt.Errorf("PORTAL_DISABLE_SCOPE must be portal.identity_mapping.disable")
		}
		for key, value := range map[string]string{"PORTAL_PROVISION_URL": config.PortalProvisionURL, "PORTAL_PROVISION_TOKEN_URL": config.PortalProvisionTokenURL, "PORTAL_PROVISION_CLIENT_ID": config.PortalProvisionClientID, "PORTAL_PROVISION_CLIENT_SECRET": config.PortalProvisionClientSecret} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when PORTAL_INVITE_ENABLED=true", key)
			}
		}
		for key, value := range map[string]string{"PORTAL_DISABLE_URL": config.PortalDisableURL, "PORTAL_DISABLE_CLIENT_ID": config.PortalDisableClientID, "PORTAL_DISABLE_CLIENT_SECRET": config.PortalDisableClientSecret} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when PORTAL_INVITE_ENABLED=true", key)
			}
		}
		for key, value := range map[string]string{"PORTAL_PROVISION_URL": config.PortalProvisionURL, "PORTAL_DISABLE_URL": config.PortalDisableURL, "PORTAL_PROVISION_TOKEN_URL": config.PortalProvisionTokenURL} {
			parsed, err := url.ParseRequestURI(value)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
		if err := config.PortalProvisionTLS.ValidateEndpoints(config.PortalProvisionTokenURL, config.PortalProvisionURL, config.PortalDisableURL); err != nil {
			return Config{}, fmt.Errorf("Portal provision TLS: %w", err)
		}
		if !config.PlatformExternalIdentityEnabled {
			return Config{}, fmt.Errorf("PLATFORM_EXTERNAL_IDENTITY_ENABLED must be true when PORTAL_INVITE_ENABLED=true")
		}
	}
	if config.PortalMappingPlatformOnly && !config.PortalMappingDualWrite {
		return Config{}, fmt.Errorf("PORTAL_MAPPING_PLATFORM_ONLY requires PORTAL_MAPPING_DUAL_WRITE")
	}
	if config.PortalMappingDualWrite {
		if !config.PlatformExternalIdentityEnabled || !config.PortalInviteEnabled {
			return Config{}, fmt.Errorf("PORTAL_MAPPING_DUAL_WRITE requires PORTAL_INVITE_ENABLED and PLATFORM_EXTERNAL_IDENTITY_ENABLED")
		}
		if strings.TrimSpace(config.PlatformCustomerBindingBaseURL) == "" {
			return Config{}, fmt.Errorf("PLATFORM_CUSTOMER_BINDING_BASE_URL is required when PORTAL_MAPPING_DUAL_WRITE=true")
		}
		parsed, err := url.ParseRequestURI(config.PlatformCustomerBindingBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("PLATFORM_CUSTOMER_BINDING_BASE_URL must be a valid HTTP(S) URL without credentials, query or fragment")
		}
		if err := config.PlatformManagementTLS.ValidateEndpoints(config.PlatformManagementTokenURL, config.PlatformCustomerBindingBaseURL); err != nil {
			return Config{}, fmt.Errorf("platform customer binding TLS: %w", err)
		}
	}
	if config.PlatformExternalIdentityEnabled {
		if config.PlatformExternalUserScope != "external_user.provision" || config.PlatformRoleAssignScope != "application_role.assign" || config.PlatformRoleRevokeScope != "application_role.revoke" {
			return Config{}, fmt.Errorf("platform external identity scopes must be exact and least-privilege")
		}
		for key, value := range map[string]string{
			"PLATFORM_EXTERNAL_USER_PROVISION_URL": config.PlatformExternalUserProvisionURL,
			"PLATFORM_APPLICATION_ROLE_ASSIGN_URL": config.PlatformApplicationRoleAssignURL,
			"PLATFORM_APPLICATION_ROLE_REVOKE_URL": config.PlatformApplicationRoleRevokeURL,
			"PLATFORM_MANAGEMENT_TOKEN_URL":        config.PlatformManagementTokenURL,
			"PLATFORM_PORTAL_APPLICATION_CODE":     config.PlatformPortalApplicationCode,
			"PLATFORM_EXTERNAL_USER_CLIENT_ID":     config.PlatformExternalUserClientID,
			"PLATFORM_EXTERNAL_USER_CLIENT_SECRET": config.PlatformExternalUserClientSecret,
			"PLATFORM_ROLE_ASSIGN_CLIENT_ID":       config.PlatformRoleAssignClientID,
			"PLATFORM_ROLE_ASSIGN_CLIENT_SECRET":   config.PlatformRoleAssignClientSecret,
			"PLATFORM_ROLE_REVOKE_CLIENT_ID":       config.PlatformRoleRevokeClientID,
			"PLATFORM_ROLE_REVOKE_CLIENT_SECRET":   config.PlatformRoleRevokeClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when PLATFORM_EXTERNAL_IDENTITY_ENABLED=true", key)
			}
		}
		for key, value := range map[string]string{
			"PLATFORM_EXTERNAL_USER_PROVISION_URL": config.PlatformExternalUserProvisionURL,
			"PLATFORM_APPLICATION_ROLE_ASSIGN_URL": config.PlatformApplicationRoleAssignURL,
			"PLATFORM_APPLICATION_ROLE_REVOKE_URL": config.PlatformApplicationRoleRevokeURL,
			"PLATFORM_MANAGEMENT_TOKEN_URL":        config.PlatformManagementTokenURL,
		} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
		if err := config.PlatformManagementTLS.ValidateEndpoints(config.PlatformManagementTokenURL, config.PlatformExternalUserProvisionURL, config.PlatformApplicationRoleAssignURL, config.PlatformApplicationRoleRevokeURL); err != nil {
			return Config{}, fmt.Errorf("platform external identity TLS: %w", err)
		}
	}
	if config.OwnerDirectoryEnabled {
		// Keep validation order stable. Besides making startup diagnostics predictable,
		// this ensures the owner-directory endpoint is reported before the shared token
		// endpoint when the optional integration is enabled without any configuration.
		requiredOwnerDirectoryFields := []struct {
			key   string
			value string
		}{
			{"PLATFORM_OWNER_DIRECTORY_URL", config.PlatformOwnerDirectoryURL},
			{"PLATFORM_MANAGEMENT_TOKEN_URL", config.PlatformManagementTokenURL},
			{"PLATFORM_OWNER_DIRECTORY_CLIENT_ID", config.PlatformOwnerDirectoryClientID},
			{"PLATFORM_OWNER_DIRECTORY_CLIENT_SECRET", config.PlatformOwnerDirectorySecret},
		}
		for _, field := range requiredOwnerDirectoryFields {
			if strings.TrimSpace(field.value) == "" {
				return Config{}, fmt.Errorf("%s is required when OWNER_DIRECTORY_ENABLED=true", field.key)
			}
		}
		if config.PlatformOwnerDirectoryScope != "owner_directory.read" {
			return Config{}, fmt.Errorf("PLATFORM_OWNER_DIRECTORY_SCOPE must be owner_directory.read")
		}
		for key, value := range map[string]string{
			"PLATFORM_OWNER_DIRECTORY_URL":  config.PlatformOwnerDirectoryURL,
			"PLATFORM_MANAGEMENT_TOKEN_URL": config.PlatformManagementTokenURL,
		} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
		if err := config.PlatformManagementTLS.ValidateEndpoints(config.PlatformManagementTokenURL, config.PlatformOwnerDirectoryURL); err != nil {
			return Config{}, fmt.Errorf("platform owner directory TLS: %w", err)
		}
	}
	if config.ApprovalTaskResolverEnabled {
		for key, value := range map[string]string{
			"APPROVAL_TASK_URL": config.ApprovalTaskURL, "APPROVAL_TASK_TOKEN_URL": config.ApprovalTaskTokenURL,
			"APPROVAL_TASK_CLIENT_ID": config.ApprovalTaskClientID, "APPROVAL_TASK_CLIENT_SECRET": config.ApprovalTaskClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when APPROVAL_TASK_RESOLVER_ENABLED=true", key)
			}
		}
		if config.ApprovalTaskScope != "presale.approval.task.read" {
			return Config{}, fmt.Errorf("APPROVAL_TASK_SCOPE must be presale.approval.task.read")
		}
		for key, value := range map[string]string{"APPROVAL_TASK_URL": config.ApprovalTaskURL, "APPROVAL_TASK_TOKEN_URL": config.ApprovalTaskTokenURL} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTPS URL without credentials, query or fragment", key)
			}
		}
		if err := config.ApprovalTaskTLS.ValidateEndpoints(config.ApprovalTaskTokenURL, config.ApprovalTaskURL); err != nil {
			return Config{}, fmt.Errorf("approval task TLS: %w", err)
		}
	}
	if config.PortalProjectHistoryEnabled {
		for key, value := range map[string]string{
			"PORTAL_PROJECT_HISTORY_URL":           config.PortalProjectHistoryURL,
			"PORTAL_PROJECT_HISTORY_TOKEN_URL":     config.PortalProjectHistoryTokenURL,
			"PORTAL_PROJECT_HISTORY_CLIENT_ID":     config.PortalProjectHistoryClientID,
			"PORTAL_PROJECT_HISTORY_CLIENT_SECRET": config.PortalProjectHistoryClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when PORTAL_PROJECT_HISTORY_ENABLED=true", key)
			}
		}
		if config.PortalProjectHistoryScope != "portal.project_history.read" {
			return Config{}, fmt.Errorf("PORTAL_PROJECT_HISTORY_SCOPE must be portal.project_history.read")
		}
		for key, value := range map[string]string{"PORTAL_PROJECT_HISTORY_URL": config.PortalProjectHistoryURL, "PORTAL_PROJECT_HISTORY_TOKEN_URL": config.PortalProjectHistoryTokenURL} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
	}
	if config.ContractVerificationEnabled {
		for key, value := range map[string]string{
			"CONTRACT_SUMMARY_URL": config.ContractSummaryURL, "CONTRACT_SUMMARY_TOKEN_URL": config.ContractSummaryTokenURL,
			"CONTRACT_SUMMARY_CLIENT_ID": config.ContractSummaryClientID, "CONTRACT_SUMMARY_CLIENT_SECRET": config.ContractSummaryClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when CONTRACT_VERIFICATION_ENABLED=true", key)
			}
		}
		if config.ContractSummaryScope != "contract.summary.read" {
			return Config{}, fmt.Errorf("CONTRACT_SUMMARY_SCOPE must be contract.summary.read")
		}
		for key, value := range map[string]string{"CONTRACT_SUMMARY_URL": config.ContractSummaryURL, "CONTRACT_SUMMARY_TOKEN_URL": config.ContractSummaryTokenURL} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
	}
	if config.ContractSignedCountEnabled {
		for key, value := range map[string]string{
			"CONTRACT_SIGNED_COUNT_URL":           config.ContractSignedCountURL,
			"CONTRACT_SIGNED_COUNT_TOKEN_URL":     config.ContractSignedCountTokenURL,
			"CONTRACT_SIGNED_COUNT_CLIENT_ID":     config.ContractSignedCountClientID,
			"CONTRACT_SIGNED_COUNT_CLIENT_SECRET": config.ContractSignedCountClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when CONTRACT_SIGNED_COUNT_ENABLED=true", key)
			}
		}
		if config.ContractSignedCountScope != "contract.opportunity_signed_count.read" {
			return Config{}, fmt.Errorf("CONTRACT_SIGNED_COUNT_SCOPE must be contract.opportunity_signed_count.read")
		}
		for key, value := range map[string]string{
			"CONTRACT_SIGNED_COUNT_URL":       config.ContractSignedCountURL,
			"CONTRACT_SIGNED_COUNT_TOKEN_URL": config.ContractSignedCountTokenURL,
		} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
	}
	if config.QBStatusQueryEnabled {
		for key, value := range map[string]string{
			"QB_STATUS_URL": config.QBStatusURL, "QB_STATUS_TOKEN_URL": config.QBStatusTokenURL,
			"QB_STATUS_CLIENT_ID": config.QBStatusClientID, "QB_STATUS_CLIENT_SECRET": config.QBStatusClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when QB_STATUS_QUERY_ENABLED=true", key)
			}
		}
		if config.QBStatusScope != "opportunity.status.read" {
			return Config{}, fmt.Errorf("QB_STATUS_SCOPE must be opportunity.status.read")
		}
		for key, value := range map[string]string{"QB_STATUS_URL": config.QBStatusURL, "QB_STATUS_TOKEN_URL": config.QBStatusTokenURL} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid HTTPS URL without credentials, query or fragment", key)
			}
		}
		if err := config.QBStatusTLS.ValidateEndpoints(config.QBStatusURL, config.QBStatusTokenURL); err != nil {
			return Config{}, fmt.Errorf("QB status TLS: %w", err)
		}
	}
	if config.QBLaunchEnabled {
		if len(config.QBLaunchSigningKey) < 32 {
			return Config{}, fmt.Errorf("QB_LAUNCH_SIGNING_KEY_BASE64 must decode to at least 32 bytes")
		}
		if config.QBLaunchTTL <= 0 || config.QBLaunchTTL > 5*time.Minute {
			return Config{}, fmt.Errorf("QB_LAUNCH_TTL must be positive and at most 5m")
		}
		for key, value := range map[string]string{"QB_QUOTATION_PUBLIC_URL": config.QBQuotationPublicURL, "QB_BID_PUBLIC_URL": config.QBBidPublicURL} {
			parsed, parseErr := url.ParseRequestURI(value)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("%s must be a valid public HTTPS URL without credentials, query or fragment", key)
			}
		}
	}
	if config.CatalogSyncEnabled {
		for key, value := range map[string]string{
			"PLATFORM_BASE_URL":                             config.PlatformBaseURL,
			"PLATFORM_AUTHORIZATION_CATALOG_APPLICATION_ID": config.CatalogApplicationID,
			"PLATFORM_AUTHORIZATION_CATALOG_CLIENT_ID":      config.CatalogClientID,
			"PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET":  config.CatalogClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED=true", key)
			}
		}
		if err := validateHTTPOrigin("PLATFORM_BASE_URL", config.PlatformBaseURL); err != nil {
			return Config{}, err
		}
	}
	if config.PathPrefix == "/" || !strings.HasPrefix(config.PathPrefix, "/") || strings.HasSuffix(config.PathPrefix, "/") {
		return Config{}, fmt.Errorf("APP_PATH_PREFIX must be a non-root absolute path without trailing slash")
	}
	if err := config.validateOIDC(); err != nil {
		return Config{}, err
	}
	if config.CatalogSyncEnabled && config.OIDCRoleConfigHash == "" {
		return Config{}, fmt.Errorf("OIDC_ROLE_CONFIG_HASH is required for CRM authorization catalog compatibility")
	}
	return config, nil
}

func (c Config) validateOIDC() error {
	// OIDC、浏览器 Origin 和机器令牌材料作为一个生产认证契约整体校验，防止只配置登录
	// 却留下内部接口无验签，或 Cookie 在非回环明文 HTTP 上传输。
	required := map[string]string{"OIDC_ISSUER": c.OIDCIssuer, "OIDC_CLIENT_ID": c.OIDCClientID, "OIDC_CLIENT_SECRET": c.OIDCClientSecret, "OIDC_REDIRECT_URI": c.OIDCRedirectURI, "OIDC_TENANT_ID": c.OIDCTenantID, "OIDC_ROLE_CONFIG_HASH": c.OIDCRoleConfigHash, "OIDC_SESSION_COOKIE_NAME": c.OIDCSessionCookieName, "MACHINE_TOKEN_ISSUER": c.MachineTokenIssuer, "MACHINE_TOKEN_AUDIENCE": c.MachineTokenAudience, "MACHINE_TOKEN_PUBLIC_KEY_PATH": c.MachineTokenPublicKeyPath}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if c.OIDCSessionTTL <= 0 || c.OIDCSessionTTL > maxOIDCSessionTTL {
		return fmt.Errorf("OIDC_SESSION_TTL must be positive and at most %s", maxOIDCSessionTTL)
	}
	if c.OIDCMaxRoles <= 0 || c.OIDCMaxRoles > 10 {
		return fmt.Errorf("OIDC_MAX_EFFECTIVE_ROLES must be between 1 and 10")
	}
	if !contains(c.OIDCScopes, "openid") {
		return fmt.Errorf("OIDC_SCOPES must include openid")
	}
	for key, value := range map[string]string{"OIDC_ISSUER": c.OIDCIssuer, "OIDC_REDIRECT_URI": c.OIDCRedirectURI} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL", key)
		}
	}
	publicOrigin, err := url.ParseRequestURI(c.PublicOrigin)
	if err != nil || (publicOrigin.Scheme != "http" && publicOrigin.Scheme != "https") || publicOrigin.Host == "" || publicOrigin.User != nil || (publicOrigin.Path != "" && publicOrigin.Path != "/") || publicOrigin.RawQuery != "" || publicOrigin.Fragment != "" {
		return fmt.Errorf("APP_PUBLIC_ORIGIN must be an HTTP(S) origin in production mode")
	}
	if !c.OIDCSessionSecure && !isLoopbackHTTPOrigin(publicOrigin) && !c.AllowInsecureHTTPSession {
		return fmt.Errorf("OIDC_SESSION_COOKIE_SECURE may be false only for a loopback HTTP APP_PUBLIC_ORIGIN")
	}
	if c.OIDCBackchannelBaseURL != "" {
		// 后通道地址仅替换容器内网络目的地，令牌中的 issuer 仍必须匹配公开地址；因此这里只
		// 接受纯 origin，禁止路径、查询和凭据改变 Discovery/JWKS 的协议语义。
		parsed, err := url.ParseRequestURI(c.OIDCBackchannelBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("OIDC_BACKCHANNEL_BASE_URL must be an HTTP(S) origin")
		}
	}
	return nil
}

func isLoopbackHTTPOrigin(origin *url.URL) bool {
	if origin == nil || origin.Scheme != "http" {
		return false
	}
	host := origin.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitFields(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == ',' })
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validateHTTPOrigin(key, value string) error {
	// 服务根地址用于拼接固定管理端点，禁止携带路径和查询，避免客户端凭据被发送到意外位置。
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an HTTP(S) origin", key)
	}
	return nil
}
