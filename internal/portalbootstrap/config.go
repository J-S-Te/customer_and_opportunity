package portalbootstrap

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Portal 本地会话最多缓存授权十五分钟，且不能超过上游令牌的实际有效期。
const maxSessionTTL = 15 * time.Minute

type Config struct {
	Address                   string
	MySQLDSN                  string
	PathPrefix                string
	PublicOrigin              string
	TenantID                  string
	RoleConfigHash            string
	OIDCIssuer                string
	OIDCBackchannelURL        string
	OIDCClientID              string
	OIDCClientSecret          string
	OIDCRedirectURI           string
	OIDCScopes                []string
	SessionCookieName         string
	SessionCookieSecure       bool
	SessionTTL                time.Duration
	AccountSecurityCenterURL  string
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
	CatalogSyncEnabled        bool
	CatalogApplicationID      string
	CatalogClientID           string
	CatalogClientSecret       string
}

func LoadConfig() (Config, error) {
	// 所有安全相关配置在启动时冻结；解析或校验失败即退出，运行时不从环境变量做隐式降级。
	secure, err := strconv.ParseBool(valueOrDefault("PORTAL_SESSION_COOKIE_SECURE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_SESSION_COOKIE_SECURE: %w", err)
	}
	ttl, err := time.ParseDuration(valueOrDefault("PORTAL_SESSION_TTL", "15m"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_SESSION_TTL: %w", err)
	}
	projectHistoryStaleAfter, err := time.ParseDuration(valueOrDefault("PORTAL_PROJECT_HISTORY_STALE_AFTER", "10m"))
	if err != nil {
		return Config{}, fmt.Errorf("PORTAL_PROJECT_HISTORY_STALE_AFTER: %w", err)
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
	config := Config{
		Address: valueOrDefault("PORTAL_HTTP_ADDRESS", ":8091"), MySQLDSN: os.Getenv("PORTAL_MYSQL_DSN"),
		PathPrefix: valueOrDefault("PORTAL_PATH_PREFIX", "/customer-portal"), PublicOrigin: os.Getenv("PORTAL_PUBLIC_ORIGIN"),
		TenantID: os.Getenv("PORTAL_OIDC_TENANT_ID"), RoleConfigHash: os.Getenv("PORTAL_ROLE_CONFIG_HASH"),
		OIDCIssuer: os.Getenv("PORTAL_OIDC_ISSUER"), OIDCBackchannelURL: os.Getenv("PORTAL_OIDC_BACKCHANNEL_BASE_URL"),
		OIDCClientID: os.Getenv("PORTAL_OIDC_CLIENT_ID"), OIDCClientSecret: os.Getenv("PORTAL_OIDC_CLIENT_SECRET"),
		OIDCRedirectURI: os.Getenv("PORTAL_OIDC_REDIRECT_URI"), OIDCScopes: fields(valueOrDefault("PORTAL_OIDC_SCOPES", "openid profile")),
		SessionCookieName: valueOrDefault("PORTAL_SESSION_COOKIE_NAME", "customer_portal_session"), SessionCookieSecure: secure, SessionTTL: ttl,
		AccountSecurityCenterURL: os.Getenv("PORTAL_ACCOUNT_SECURITY_CENTER_URL"),
		MachineTokenIssuer:       os.Getenv("PORTAL_MACHINE_TOKEN_ISSUER"), MachineTokenAudience: os.Getenv("PORTAL_MACHINE_TOKEN_AUDIENCE"),
		MachineTokenPublicKeyPath: os.Getenv("PORTAL_MACHINE_TOKEN_PUBLIC_KEY_PATH"),
		CRMProvisionClientSubject: os.Getenv("PORTAL_CRM_PROVISION_CLIENT_SUBJECT"), CRMDisableClientSubject: os.Getenv("PORTAL_CRM_DISABLE_CLIENT_SUBJECT"),
		ProjectHistoryStaleAfter: projectHistoryStaleAfter, CRMInviteBaseURL: os.Getenv("PORTAL_CRM_INVITE_BASE_URL"),
		CRMInviteTokenURL: os.Getenv("PORTAL_CRM_INVITE_TOKEN_URL"), CRMInviteClientID: os.Getenv("PORTAL_CRM_INVITE_CLIENT_ID"), CRMInviteClientSecret: os.Getenv("PORTAL_CRM_INVITE_CLIENT_SECRET"),
		CRMInviteScope: valueOrDefault("PORTAL_CRM_INVITE_SCOPE", "portal.invite.verify"),
		EncryptionKey:  encryptionKey, ReportIngestDescriptorKey: reportIngestDescriptorKey, HMACKey: hmacKey,
		PlatformBaseURL: os.Getenv("PORTAL_PLATFORM_BASE_URL"), CatalogSyncEnabled: catalogSyncEnabled,
		CatalogApplicationID: os.Getenv("PORTAL_AUTHORIZATION_CATALOG_APPLICATION_ID"), CatalogClientID: os.Getenv("PORTAL_AUTHORIZATION_CATALOG_CLIENT_ID"),
		CatalogClientSecret: os.Getenv("PORTAL_AUTHORIZATION_CATALOG_CLIENT_SECRET"),
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) validate() error {
	required := map[string]string{
		"PORTAL_MYSQL_DSN": c.MySQLDSN, "PORTAL_PUBLIC_ORIGIN": c.PublicOrigin,
		"PORTAL_OIDC_TENANT_ID": c.TenantID, "PORTAL_ROLE_CONFIG_HASH": c.RoleConfigHash,
		"PORTAL_OIDC_ISSUER": c.OIDCIssuer, "PORTAL_OIDC_CLIENT_ID": c.OIDCClientID,
		"PORTAL_OIDC_CLIENT_SECRET": c.OIDCClientSecret, "PORTAL_OIDC_REDIRECT_URI": c.OIDCRedirectURI,
		"PORTAL_ACCOUNT_SECURITY_CENTER_URL":   c.AccountSecurityCenterURL,
		"PORTAL_MACHINE_TOKEN_ISSUER":          c.MachineTokenIssuer,
		"PORTAL_MACHINE_TOKEN_AUDIENCE":        c.MachineTokenAudience,
		"PORTAL_MACHINE_TOKEN_PUBLIC_KEY_PATH": c.MachineTokenPublicKeyPath,
		"PORTAL_CRM_PROVISION_CLIENT_SUBJECT":  c.CRMProvisionClientSubject,
		"PORTAL_CRM_DISABLE_CLIENT_SUBJECT":    c.CRMDisableClientSubject,
		"PORTAL_CRM_INVITE_BASE_URL":           c.CRMInviteBaseURL,
		"PORTAL_CRM_INVITE_TOKEN_URL":          c.CRMInviteTokenURL,
		"PORTAL_CRM_INVITE_CLIENT_ID":          c.CRMInviteClientID,
		"PORTAL_CRM_INVITE_CLIENT_SECRET":      c.CRMInviteClientSecret,
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	for key, value := range map[string]string{
		"PORTAL_CRM_PROVISION_CLIENT_SUBJECT": c.CRMProvisionClientSubject,
		"PORTAL_CRM_DISABLE_CLIENT_SUBJECT":   c.CRMDisableClientSubject,
	} {
		// 已认证机器主体以 "machine:" + subject 写入共享的 64 字节 updated_by 列，故需为前缀预留空间。
		if value != strings.TrimSpace(value) || len(value) > 56 {
			return fmt.Errorf("%s must be a trimmed OAuth client subject of at most 56 bytes", key)
		}
	}
	if len(c.EncryptionKey) != 32 || len(c.ReportIngestDescriptorKey) != 32 || len(c.HMACKey) < 32 {
		return fmt.Errorf("Portal encryption keys must each decode to 32 bytes and HMAC key to at least 32 bytes")
	}
	if string(c.EncryptionKey) == string(c.ReportIngestDescriptorKey) {
		// 通用敏感数据与报告摄取描述符使用独立密钥域，避免一种密文协议中的泄漏扩大到另一域。
		return fmt.Errorf("Portal report ingest descriptor key must be distinct from the general encryption key")
	}
	if c.ProjectHistoryStaleAfter < 0 {
		return fmt.Errorf("PORTAL_PROJECT_HISTORY_STALE_AFTER must be positive")
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
		parsed, err := url.ParseRequestURI(c.PlatformBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("PORTAL_PLATFORM_BASE_URL must be an HTTP(S) origin")
		}
	}
	if c.SessionTTL <= 0 || c.SessionTTL > maxSessionTTL {
		return fmt.Errorf("PORTAL_SESSION_TTL must be positive and at most %s", maxSessionTTL)
	}
	if c.PathPrefix == "/" || !strings.HasPrefix(c.PathPrefix, "/") || strings.HasSuffix(c.PathPrefix, "/") {
		return fmt.Errorf("PORTAL_PATH_PREFIX must be a non-root absolute path without trailing slash")
	}
	if !contains(c.OIDCScopes, "openid") {
		return fmt.Errorf("PORTAL_OIDC_SCOPES must include openid")
	}
	for key, value := range map[string]string{"PORTAL_PUBLIC_ORIGIN": c.PublicOrigin, "PORTAL_OIDC_ISSUER": c.OIDCIssuer, "PORTAL_OIDC_REDIRECT_URI": c.OIDCRedirectURI} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL", key)
		}
	}
	securityCenter, err := url.ParseRequestURI(c.AccountSecurityCenterURL)
	// 安全中心链接会返回浏览器，生产仅允许 HTTPS；回环 HTTP 只服务本机开发。
	if err != nil || securityCenter.Host == "" || securityCenter.User != nil || securityCenter.RawQuery != "" || securityCenter.Fragment != "" ||
		(securityCenter.Scheme != "https" && !(securityCenter.Scheme == "http" && loopbackHostname(securityCenter.Hostname()))) {
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
func (c Config) projectHistoryStalenessThreshold() time.Duration {
	if c.ProjectHistoryStaleAfter == 0 {
		return 10 * time.Minute
	}
	return c.ProjectHistoryStaleAfter
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
