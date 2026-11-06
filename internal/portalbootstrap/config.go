package portalbootstrap

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
)

// Portal 本地会话最多缓存授权十五分钟，且不能超过上游令牌的实际有效期。
const maxSessionTTL = 15 * time.Minute

// 匿名登录入口限流的默认值与可用区间（M-10）。限流器是进程内固定窗口
// （见 rate_limit.go）：只保证单副本额度，多副本部署必须由入口网关
// limit_req 兜底，且桶键依赖可信代理配置才能还原真实客户端地址。
const (
	defaultLoginRateLimit      = 20
	defaultActivateRateLimit   = 20
	defaultCallbackRateLimit   = 30
	defaultAuthRateLimitWindow = time.Minute
	maxAuthRateLimit           = 600
	minAuthRateLimitWindow     = time.Second
	maxAuthRateLimitWindow     = time.Hour
)

type Config struct {
	LocalDirectoryMode                                                                       string
	LocalInviteHMACKey                                                                       []byte
	LocalPlatformTokenURL                                                                    string
	LocalProvision, LocalRoleAssign, LocalRoleRevoke, LocalBindingWrite, LocalBindingDisable LocalIdentityCredential
	FileGatewayApplicationID, ReportSourceApplicationID                                      string

	CentralSessionEnabled       bool
	CentralSessionPublicBaseURL string
	Address                     string
	MySQLDSN                    string
	PathPrefix                  string
	PublicOrigin                string
	TenantID                    string
	RoleConfigHash              string
	OIDCIssuer                  string
	OIDCBackchannelURL          string
	OIDCClientID                string
	OIDCClientSecret            string
	OIDCRedirectURI             string
	OIDCScopes                  []string
	OIDCIdentityProviderHint    string
	SessionCookieName           string
	SessionCookieSecure         bool
	SessionTTL                  time.Duration
	BackchannelLogoutTTL        time.Duration
	AccountSecurityCenterURL    string
	// 匿名登录入口的进程内固定窗口限流额度（每 RateLimitWindow 每客户端地址）。
	// 0 表示取默认值；不等于关闭限流。
	LoginRateLimit    int
	ActivateRateLimit int
	CallbackRateLimit int
	RateLimitWindow   time.Duration
	// AllowInsecureHTTPSession 仅用于无 HTTPS 的测试服务器：显式开启后允许非回环 HTTP
	// 源使用非 Secure 会话 Cookie 和非 HTTPS 安全中心链接。默认关闭，生产必须保持 false。
	AllowInsecureHTTPSession  bool
	MachineTokenIssuer        string
	MachineTokenAudience      string
	MachineTokenPublicKeyPath string
	CRMProvisionClientSubject string
	CRMDisableClientSubject   string
	ProjectHistoryStaleAfter  time.Duration
	CRMInviteBaseURL          string
	CRMInviteTokenURL         string
	CRMInviteClientID         string
	CRMInviteClientSecret     string
	CRMInviteScope            string
	EncryptionKey             []byte
	ReportIngestDescriptorKey []byte
	HMACKey                   []byte
	PlatformBaseURL           string
	PlatformApplicationCode   string
	// TrustedProxies 是唯一允许采信转发头的直接对端（AUD-IP-2026，默认仅回环）。
	TrustedProxies            []netip.Prefix
	PlatformEnvironmentCode   string
	PlatformAuditClientID     string
	PlatformAuditClientSecret string
	PlatformAuditWorkerID     string
	PlatformAuditPollInterval time.Duration
	PlatformAuditBatchSize    int
	CatalogSyncEnabled        bool
	CatalogApplicationID      string
	CatalogClientID           string
	CatalogClientSecret       string
	// UsePlatformBinding 打开后，非邀请登录以平台 authorization-context 的 customer_ref
	// 作为客户边界（Phase 4），本地 portal_identity_links 仅作邀请链路与回退。
	UsePlatformBinding bool
	// FileGatewayLocalEnabled 启用本地文件网关时，报告文件仅从该根目录读取。
	FileGatewayLocalEnabled    bool
	FileGatewayLocalRoot       string
	FileGatewayBaseURL         string
	FileGatewayClientID        string
	FileGatewayClientSecret    string
	FileGatewayScope           string
	PDFConfigRoot              string
	CJKFontPath                string
	ReportContentKey           []byte
	ReportStoreRoot            string
	ReportIdentityURL          string
	ReportIdentityClientID     string
	ReportIdentityClientSecret string
}

// LoadConfig 读取并校验应用运行配置及中央接入开关。
// 参数：无。
// 返回值：校验后的运行配置及解析或约束错误。
func LoadConfig() (Config, error) {
	// 所有安全相关配置在启动时冻结；解析或校验失败即退出，运行时不从环境变量做隐式降级。
	secure, err := strconv.ParseBool(valueOrDefault("PORTAL_SESSION_COOKIE_SECURE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_SESSION_COOKIE_SECURE: %w", err)
	}
	allowInsecureHTTPSession, err := strconv.ParseBool(valueOrDefault("PORTAL_ALLOW_INSECURE_HTTP_SESSION", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_ALLOW_INSECURE_HTTP_SESSION: %w", err)
	}
	centralEnabled, err := strconv.ParseBool(valueOrDefault("PORTAL_CENTRAL_SESSION_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_CENTRAL_SESSION_ENABLED: %w", err)
	}
	ttl, err := time.ParseDuration(valueOrDefault("PORTAL_SESSION_TTL", "15m"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_SESSION_TTL: %w", err)
	}
	backchannelLogoutTTL, err := time.ParseDuration(valueOrDefault("PORTAL_OIDC_BACKCHANNEL_LOGOUT_MAX_TTL", "5m"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_OIDC_BACKCHANNEL_LOGOUT_MAX_TTL: %w", err)
	}
	projectHistoryStaleAfter, err := time.ParseDuration(valueOrDefault("PORTAL_PROJECT_HISTORY_STALE_AFTER", "10m"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_PROJECT_HISTORY_STALE_AFTER: %w", err)
	}
	platformAuditPollInterval, err := time.ParseDuration(valueOrDefault("PLATFORM_AUDIT_POLL_INTERVAL", "1s"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_AUDIT_POLL_INTERVAL: %w", err)
	}
	platformAuditBatchSize, err := strconv.Atoi(valueOrDefault("PLATFORM_AUDIT_BATCH_SIZE", "100"))
	if err != nil {
		return Config{}, fmt.Errorf("PLATFORM_AUDIT_BATCH_SIZE: %w", err)
	}
	encryptionKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_ENCRYPTION_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_ENCRYPTION_KEY_BASE64: %w", err)
	}
	reportIngestDescriptorKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_REPORT_INGEST_DESCRIPTOR_KEY_BASE64: %w", err)
	}
	hmacKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_HMAC_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_HMAC_KEY_BASE64: %w", err)
	}
	catalogSyncEnabled, err := strconv.ParseBool(valueOrDefault("PORTAL_AUTHORIZATION_CATALOG_SYNC_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_AUTHORIZATION_CATALOG_SYNC_ENABLED: %w", err)
	}
	usePlatformBinding, err := strconv.ParseBool(valueOrDefault("PORTAL_USE_PLATFORM_BINDING", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_USE_PLATFORM_BINDING: %w", err)
	}
	fileGatewayLocalEnabled, err := strconv.ParseBool(valueOrDefault("PORTAL_FILE_GATEWAY_LOCAL_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_FILE_GATEWAY_LOCAL_ENABLED: %w", err)
	}
	reportContentKey, err := base64.StdEncoding.DecodeString(os.Getenv("PORTAL_REPORT_CONTENT_KEY_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_REPORT_CONTENT_KEY_BASE64 is invalid")
	}
	trustedProxies, err := loginip.ParseTrusted(valueOrDefault("APP_TRUSTED_PROXIES", "127.0.0.1/32,::1/128"))
	if err != nil {
		return Config{}, fmt.Errorf("APP_TRUSTED_PROXIES: %w", err)
	}
	// 登录入口限流额度：缺省使用内置默认值，解析失败直接退出，避免带病启动。
	loginRateLimit, err := intValueOrDefault("PORTAL_LOGIN_RATE_LIMIT", defaultLoginRateLimit)
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_LOGIN_RATE_LIMIT: %w", err)
	}
	activateRateLimit, err := intValueOrDefault("PORTAL_ACTIVATE_RATE_LIMIT", defaultActivateRateLimit)
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_ACTIVATE_RATE_LIMIT: %w", err)
	}
	callbackRateLimit, err := intValueOrDefault("PORTAL_CALLBACK_RATE_LIMIT", defaultCallbackRateLimit)
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_CALLBACK_RATE_LIMIT: %w", err)
	}
	rateLimitWindow, err := time.ParseDuration(valueOrDefault("PORTAL_AUTH_RATE_LIMIT_WINDOW", defaultAuthRateLimitWindow.String()))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_AUTH_RATE_LIMIT_WINDOW: %w", err)
	}
	config := Config{
		CentralSessionEnabled:       centralEnabled,
		CentralSessionPublicBaseURL: valueOrDefault("PORTAL_CENTRAL_SESSION_PUBLIC_BASE_URL", os.Getenv("PORTAL_PUBLIC_ORIGIN")),
		Address:                     valueOrDefault("PORTAL_HTTP_ADDRESS", ":8091"), MySQLDSN: os.Getenv("PORTAL_MYSQL_DSN"),
		PathPrefix: valueOrDefault("PORTAL_PATH_PREFIX", "/customer-portal"), PublicOrigin: os.Getenv("PORTAL_PUBLIC_ORIGIN"),
		TenantID: os.Getenv("PORTAL_OIDC_TENANT_ID"), RoleConfigHash: os.Getenv("PORTAL_ROLE_CONFIG_HASH"),
		OIDCIssuer: os.Getenv("PORTAL_OIDC_ISSUER"), OIDCBackchannelURL: os.Getenv("PORTAL_OIDC_BACKCHANNEL_BASE_URL"),
		OIDCClientID: os.Getenv("PORTAL_OIDC_CLIENT_ID"), OIDCClientSecret: os.Getenv("PORTAL_OIDC_CLIENT_SECRET"),
		OIDCRedirectURI: os.Getenv("PORTAL_OIDC_REDIRECT_URI"), OIDCScopes: fields(valueOrDefault("PORTAL_OIDC_SCOPES", "openid profile")),
		OIDCIdentityProviderHint: portalOIDCIdentityProviderHint(),
		SessionCookieName:        valueOrDefault("PORTAL_SESSION_COOKIE_NAME", "customer_portal_session"), SessionCookieSecure: secure, SessionTTL: ttl, BackchannelLogoutTTL: backchannelLogoutTTL,
		AllowInsecureHTTPSession: allowInsecureHTTPSession,
		AccountSecurityCenterURL: os.Getenv("PORTAL_ACCOUNT_SECURITY_CENTER_URL"),
		LoginRateLimit:           loginRateLimit, ActivateRateLimit: activateRateLimit,
		CallbackRateLimit: callbackRateLimit, RateLimitWindow: rateLimitWindow,
		MachineTokenIssuer: os.Getenv("PORTAL_MACHINE_TOKEN_ISSUER"), MachineTokenAudience: os.Getenv("PORTAL_MACHINE_TOKEN_AUDIENCE"),
		MachineTokenPublicKeyPath: os.Getenv("PORTAL_MACHINE_TOKEN_PUBLIC_KEY_PATH"),
		CRMProvisionClientSubject: os.Getenv("PORTAL_CRM_PROVISION_CLIENT_SUBJECT"), CRMDisableClientSubject: os.Getenv("PORTAL_CRM_DISABLE_CLIENT_SUBJECT"),
		ProjectHistoryStaleAfter: projectHistoryStaleAfter, CRMInviteBaseURL: os.Getenv("PORTAL_CRM_INVITE_BASE_URL"),
		CRMInviteTokenURL: os.Getenv("PORTAL_CRM_INVITE_TOKEN_URL"), CRMInviteClientID: os.Getenv("PORTAL_CRM_INVITE_CLIENT_ID"), CRMInviteClientSecret: os.Getenv("PORTAL_CRM_INVITE_CLIENT_SECRET"),
		CRMInviteScope: valueOrDefault("PORTAL_CRM_INVITE_SCOPE", "portal.invite.verify"),
		EncryptionKey:  encryptionKey, ReportIngestDescriptorKey: reportIngestDescriptorKey, HMACKey: hmacKey,
		PlatformBaseURL:            os.Getenv("PORTAL_PLATFORM_BASE_URL"),
		PlatformApplicationCode:    valueOrDefault("PLATFORM_APPLICATION_CODE", "customer_portal"),
		PlatformEnvironmentCode:    valueOrDefault("PLATFORM_ENVIRONMENT_CODE", "dev"),
		TrustedProxies:             trustedProxies,
		PlatformAuditClientID:      os.Getenv("PLATFORM_AUDIT_CLIENT_ID"),
		PlatformAuditClientSecret:  os.Getenv("PLATFORM_AUDIT_CLIENT_SECRET"),
		PlatformAuditWorkerID:      valueOrDefault("PLATFORM_AUDIT_WORKER_ID", "portal-api-audit"),
		PlatformAuditPollInterval:  platformAuditPollInterval,
		PlatformAuditBatchSize:     platformAuditBatchSize,
		CatalogSyncEnabled:         catalogSyncEnabled,
		UsePlatformBinding:         usePlatformBinding,
		FileGatewayLocalEnabled:    fileGatewayLocalEnabled,
		FileGatewayLocalRoot:       valueOrDefault("PORTAL_FILE_GATEWAY_LOCAL_ROOT", "/app/data/file-gateway"),
		FileGatewayBaseURL:         strings.TrimSpace(os.Getenv("FILE_GATEWAY_BASE_URL")),
		FileGatewayClientID:        strings.TrimSpace(os.Getenv("FILE_GATEWAY_CLIENT_ID")),
		FileGatewayClientSecret:    os.Getenv("FILE_GATEWAY_CLIENT_SECRET"),
		FileGatewayScope:           valueOrDefault("FILE_GATEWAY_SCOPE", "platform:file:upload platform:file:download"),
		PDFConfigRoot:              strings.TrimSpace(os.Getenv("PORTAL_PDF_CONFIG_ROOT")),
		CJKFontPath:                strings.TrimSpace(os.Getenv("PORTAL_CJK_FONT_PATH")),
		ReportContentKey:           reportContentKey,
		ReportStoreRoot:            strings.TrimSpace(os.Getenv("PORTAL_REPORT_STORE_ROOT")),
		ReportIdentityURL:          strings.TrimSpace(os.Getenv("PORTAL_REPORT_IDENTITY_URL")),
		ReportIdentityClientID:     strings.TrimSpace(os.Getenv("PORTAL_REPORT_IDENTITY_CLIENT_ID")),
		ReportIdentityClientSecret: os.Getenv("PORTAL_REPORT_IDENTITY_CLIENT_SECRET"),
		CatalogApplicationID:       os.Getenv("PORTAL_AUTHORIZATION_CATALOG_APPLICATION_ID"), CatalogClientID: os.Getenv("PORTAL_AUTHORIZATION_CATALOG_CLIENT_ID"),
		CatalogClientSecret: os.Getenv("PORTAL_AUTHORIZATION_CATALOG_CLIENT_SECRET"),
	}
	if err := loadLocalIdentityConfig(&config); err != nil {
		return Config{}, err
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func portalOIDCIdentityProviderHint() string {
	return valueOrDefault("PORTAL_OIDC_IDP_HINT", "basic-platform-customer")
}

// validate 核验运行配置及中央会话接入前置条件。
// 参数：无。
// 返回值：无；参数、校验、存储或依赖失败时返回错误。
func (c Config) validate() error {
	mode, modeErr := localidentity.ParseMode(c.LocalDirectoryMode)
	if modeErr != nil {
		return modeErr
	}
	if mode == localidentity.ModeLocal {
		if err := validateLocalIdentityConfig(c); err != nil {
			return err
		}
	} else if err := validateReportDownloadConfig(c); err != nil {
		return fmt.Errorf("Portal report download configuration: %w", err)
	}
	if (c.PDFConfigRoot == "") != (c.CJKFontPath == "") {
		return fmt.Errorf("PORTAL_PDF_CONFIG_ROOT and PORTAL_CJK_FONT_PATH must be configured together")
	}
	required := map[string]string{
		"PORTAL_MYSQL_DSN": c.MySQLDSN, "PORTAL_PUBLIC_ORIGIN": c.PublicOrigin,
		"PORTAL_OIDC_TENANT_ID": c.TenantID, "PORTAL_ROLE_CONFIG_HASH": c.RoleConfigHash,
		"PORTAL_OIDC_ISSUER": c.OIDCIssuer, "PORTAL_OIDC_CLIENT_ID": c.OIDCClientID,
		"PORTAL_OIDC_CLIENT_SECRET": c.OIDCClientSecret, "PORTAL_OIDC_REDIRECT_URI": c.OIDCRedirectURI,
		"PORTAL_ACCOUNT_SECURITY_CENTER_URL":   c.AccountSecurityCenterURL,
		"PORTAL_MACHINE_TOKEN_ISSUER":          c.MachineTokenIssuer,
		"PORTAL_MACHINE_TOKEN_AUDIENCE":        c.MachineTokenAudience,
		"PORTAL_MACHINE_TOKEN_PUBLIC_KEY_PATH": c.MachineTokenPublicKeyPath,
		"PORTAL_PLATFORM_BASE_URL":             c.PlatformBaseURL,
		"PLATFORM_AUDIT_CLIENT_ID":             c.PlatformAuditClientID,
		"PLATFORM_AUDIT_CLIENT_SECRET":         c.PlatformAuditClientSecret,
		"PLATFORM_APPLICATION_CODE":            c.PlatformApplicationCode,
		"PLATFORM_ENVIRONMENT_CODE":            c.PlatformEnvironmentCode,
		"PLATFORM_AUDIT_WORKER_ID":             c.PlatformAuditWorkerID,
	}
	if mode == localidentity.ModeCRM {
		for key, value := range map[string]string{"PORTAL_CRM_PROVISION_CLIENT_SUBJECT": c.CRMProvisionClientSubject, "PORTAL_CRM_DISABLE_CLIENT_SUBJECT": c.CRMDisableClientSubject, "PORTAL_CRM_INVITE_BASE_URL": c.CRMInviteBaseURL, "PORTAL_CRM_INVITE_TOKEN_URL": c.CRMInviteTokenURL, "PORTAL_CRM_INVITE_CLIENT_ID": c.CRMInviteClientID, "PORTAL_CRM_INVITE_CLIENT_SECRET": c.CRMInviteClientSecret} {
			required[key] = value
		}
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if mode == localidentity.ModeCRM {
		for key, value := range map[string]string{
			"PORTAL_CRM_PROVISION_CLIENT_SUBJECT": c.CRMProvisionClientSubject,
			"PORTAL_CRM_DISABLE_CLIENT_SUBJECT":   c.CRMDisableClientSubject,
		} {
			// 已认证机器主体以 "machine:" + subject 写入共享的 64 字节 updated_by 列，故需为前缀预留空间。
			if value != strings.TrimSpace(value) || len(value) > 56 {
				return fmt.Errorf("%s must be a trimmed OAuth client subject of at most 56 bytes", key)
			}
		}
	}
	if len(c.EncryptionKey) != 32 || len(c.ReportIngestDescriptorKey) != 32 || len(c.HMACKey) < 32 {
		return fmt.Errorf("Portal encryption keys must each decode to 32 bytes and HMAC key to at least 32 bytes")
	}
	if c.PlatformApplicationCode != "customer_portal" {
		return fmt.Errorf("PLATFORM_APPLICATION_CODE must be customer_portal")
	}
	if c.FileGatewayBaseURL != "" {
		parsed, parseErr := url.ParseRequestURI(c.FileGatewayBaseURL)
		if parseErr != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("FILE_GATEWAY_BASE_URL must be an HTTP(S) origin")
		}
		if c.FileGatewayClientID == "" || c.FileGatewayClientSecret == "" {
			return fmt.Errorf("FILE_GATEWAY_CLIENT_ID and FILE_GATEWAY_CLIENT_SECRET are required when FILE_GATEWAY_BASE_URL is configured")
		}
		scopes := strings.Fields(c.FileGatewayScope)
		if !contains(scopes, "platform:file:upload") || !contains(scopes, "platform:file:download") {
			return fmt.Errorf("FILE_GATEWAY_SCOPE must include platform:file:upload and platform:file:download")
		}
	}
	parsedPlatform, err := url.ParseRequestURI(c.PlatformBaseURL)
	if err != nil || (parsedPlatform.Scheme != "http" && parsedPlatform.Scheme != "https") || parsedPlatform.Host == "" || parsedPlatform.User != nil || (parsedPlatform.Path != "" && parsedPlatform.Path != "/") || parsedPlatform.RawQuery != "" || parsedPlatform.Fragment != "" {
		return fmt.Errorf("PORTAL_PLATFORM_BASE_URL must be an HTTP(S) origin")
	}
	if c.PlatformAuditPollInterval < 100*time.Millisecond || c.PlatformAuditPollInterval > time.Minute {
		return fmt.Errorf("PLATFORM_AUDIT_POLL_INTERVAL must be between 100ms and 1m")
	}
	if c.PlatformAuditBatchSize < 1 || c.PlatformAuditBatchSize > 100 {
		return fmt.Errorf("PLATFORM_AUDIT_BATCH_SIZE must be between 1 and 100")
	}
	if string(c.EncryptionKey) == string(c.ReportIngestDescriptorKey) {
		// 通用敏感数据与报告摄取描述符使用独立密钥域，避免一种密文协议中的泄漏扩大到另一域。
		return fmt.Errorf("Portal report ingest descriptor key must be distinct from the general encryption key")
	}
	if c.ProjectHistoryStaleAfter < 0 {
		return fmt.Errorf("PORTAL_PROJECT_HISTORY_STALE_AFTER must be positive")
	}
	// 0 表示采用内置默认额度；显式配置必须落在可解释区间，防止把登录入口配成
	// “无限制”（超大值）或“全站锁死”（极小值）而不被启动检查发现。
	for key, value := range map[string]int{
		"PORTAL_LOGIN_RATE_LIMIT":    c.LoginRateLimit,
		"PORTAL_ACTIVATE_RATE_LIMIT": c.ActivateRateLimit,
		"PORTAL_CALLBACK_RATE_LIMIT": c.CallbackRateLimit,
	} {
		if value < 0 || value > maxAuthRateLimit {
			return fmt.Errorf("%s must be between 0 (default) and %d requests per window", key, maxAuthRateLimit)
		}
	}
	if c.RateLimitWindow != 0 && (c.RateLimitWindow < minAuthRateLimitWindow || c.RateLimitWindow > maxAuthRateLimitWindow) {
		return fmt.Errorf("PORTAL_AUTH_RATE_LIMIT_WINDOW must be between %s and %s", minAuthRateLimitWindow, maxAuthRateLimitWindow)
	}
	if c.CatalogSyncEnabled {
		// 开启目录同步就必须完整提供发布客户端，不能把“同步失败”降级成继续使用未知版本目录。
		for key, value := range map[string]string{
			"PORTAL_PLATFORM_BASE_URL":                    c.PlatformBaseURL,
			"PORTAL_AUTHORIZATION_CATALOG_APPLICATION_ID": c.CatalogApplicationID,
			"PORTAL_AUTHORIZATION_CATALOG_CLIENT_ID":      c.CatalogClientID,
			"PORTAL_AUTHORIZATION_CATALOG_CLIENT_SECRET":  c.CatalogClientSecret,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required when PORTAL_AUTHORIZATION_CATALOG_SYNC_ENABLED=true", key)
			}
		}
	}
	maximumTTL := maxSessionTTL
	if c.CentralSessionEnabled {
		maximumTTL = 8 * time.Hour
	}
	if c.SessionTTL <= 0 || c.SessionTTL > maximumTTL {
		return fmt.Errorf("PORTAL_SESSION_TTL must be positive and at most %s", maxSessionTTL)
	}
	if c.BackchannelLogoutTTL != 0 && (c.BackchannelLogoutTTL < 30*time.Second || c.BackchannelLogoutTTL > 15*time.Minute) {
		return fmt.Errorf("PORTAL_OIDC_BACKCHANNEL_LOGOUT_MAX_TTL must be between 30s and 15m")
	}
	if c.PathPrefix == "/" || !strings.HasPrefix(c.PathPrefix, "/") || strings.HasSuffix(c.PathPrefix, "/") {
		return fmt.Errorf("PORTAL_PATH_PREFIX must be a non-root absolute path without trailing slash")
	}
	if !contains(c.OIDCScopes, "openid") {
		return fmt.Errorf("PORTAL_OIDC_SCOPES must include openid")
	}
	if c.OIDCIdentityProviderHint != strings.TrimSpace(c.OIDCIdentityProviderHint) {
		return fmt.Errorf("PORTAL_OIDC_IDP_HINT must be trimmed")
	}
	for key, value := range map[string]string{"PORTAL_PUBLIC_ORIGIN": c.PublicOrigin, "PORTAL_OIDC_ISSUER": c.OIDCIssuer, "PORTAL_OIDC_REDIRECT_URI": c.OIDCRedirectURI} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL", key)
		}
	}
	securityCenter, err := url.ParseRequestURI(c.AccountSecurityCenterURL)
	// 安全中心链接会返回浏览器，生产仅允许 HTTPS；回环 HTTP 只服务本机开发。
	// 测试服务器可显式开启 PORTAL_ALLOW_INSECURE_HTTP_SESSION 放宽为非回环 HTTP。
	if err != nil || securityCenter.Host == "" || securityCenter.User != nil || securityCenter.RawQuery != "" || securityCenter.Fragment != "" ||
		(!c.AllowInsecureHTTPSession && securityCenter.Scheme != "https" && !(securityCenter.Scheme == "http" && loopbackHostname(securityCenter.Hostname()))) {
		return fmt.Errorf("PORTAL_ACCOUNT_SECURITY_CENTER_URL must use HTTPS, except HTTP is allowed for localhost or a loopback IP")
	}
	origin, _ := url.Parse(c.PublicOrigin)
	if origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" {
		return fmt.Errorf("PORTAL_PUBLIC_ORIGIN must be an origin without path or query")
	}
	if c.OIDCBackchannelURL != "" {
		parsed, err := url.ParseRequestURI(c.OIDCBackchannelURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("PORTAL_OIDC_BACKCHANNEL_BASE_URL must be an HTTP(S) origin")
		}
	}
	if mode == localidentity.ModeCRM {
		for key, value := range map[string]string{"PORTAL_CRM_INVITE_BASE_URL": c.CRMInviteBaseURL, "PORTAL_CRM_INVITE_TOKEN_URL": c.CRMInviteTokenURL} {
			parsed, err := url.ParseRequestURI(value)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("%s must be a valid HTTP(S) URL without credentials, query or fragment", key)
			}
		}
		inviteScopes := strings.Fields(c.CRMInviteScope)
		// Portal 反向调用 CRM 只需要验证邀请，不接受组合 scope，降低凭据泄漏后的可用权限面。
		if len(inviteScopes) != 1 || inviteScopes[0] != "portal.invite.verify" {
			return fmt.Errorf("PORTAL_CRM_INVITE_SCOPE must be portal.invite.verify")
		}
	}
	return nil
}

func loopbackHostname(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	address := net.ParseIP(strings.TrimSpace(host))
	return address != nil && address.IsLoopback()
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// intValueOrDefault 用途：解析整数环境配置，缺省时使用既定额度。
// 参数：key 为环境变量名；fallback 为缺省额度。
// 返回值：解析值；非整数配置返回错误。
func intValueOrDefault(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}
func (c Config) projectHistoryStalenessThreshold() time.Duration {
	if c.ProjectHistoryStaleAfter == 0 {
		return 10 * time.Minute
	}
	return c.ProjectHistoryStaleAfter
}

// 登录入口限流参数：Config 零值直接构造（测试、嵌入方）时仍按内置默认额度生效，
// 避免“忘记配置”变成“完全不限流”。

// loginRateLimit 用途：读取登录额度，零值保持默认限流。
// 参数：无。
// 返回值：登录入口每窗口每客户端的有效额度。
func (c Config) loginRateLimit() int {
	return authRateLimitOrDefault(c.LoginRateLimit, defaultLoginRateLimit)
}

// activateRateLimit 用途：读取邀请额度，零值保持默认限流。
// 参数：无。
// 返回值：邀请入口每窗口每客户端的有效额度。
func (c Config) activateRateLimit() int {
	return authRateLimitOrDefault(c.ActivateRateLimit, defaultActivateRateLimit)
}

// callbackRateLimit 用途：读取回调额度，零值保持默认限流。
// 参数：无。
// 返回值：回调入口每窗口每客户端的有效额度。
func (c Config) callbackRateLimit() int {
	return authRateLimitOrDefault(c.CallbackRateLimit, defaultCallbackRateLimit)
}

// authRateLimitWindow 用途：读取有效固定窗口，零值使用默认窗口。
// 参数：无。
// 返回值：限流时间窗口。
func (c Config) authRateLimitWindow() time.Duration {
	if c.RateLimitWindow <= 0 {
		return defaultAuthRateLimitWindow
	}
	return c.RateLimitWindow
}

// authRateLimitOrDefault 用途：让未指定额度仍按默认值限流。
// 参数：value 为配置额度；fallback 为入口默认额度。
// 返回值：正数配置值或默认额度。
func authRateLimitOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
func fields(value string) []string {
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
