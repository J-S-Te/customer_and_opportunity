package portalbootstrap

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
)

// loadLocalIdentityConfig 读取显式本地目录配置，默认继续CRM适配。
// 参数：c为当前门户配置，仅读受控启动环境。
// 返回值：非法模式或密钥编码错误，不静默降级。
func loadLocalIdentityConfig(c *Config) error {
	mode, err := localidentity.ParseMode(os.Getenv("PORTAL_CUSTOMER_DIRECTORY_MODE"))
	if err != nil {
		return err
	}
	c.LocalDirectoryMode = string(mode)
	if raw := os.Getenv("PORTAL_LOCAL_INVITE_HMAC_KEY_BASE64"); raw != "" {
		c.LocalInviteHMACKey, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("PORTAL_LOCAL_INVITE_HMAC_KEY_BASE64: invalid base64")
		}
	}
	c.LocalPlatformTokenURL = valueOrDefault("PORTAL_LOCAL_PLATFORM_TOKEN_URL", strings.TrimRight(c.PlatformBaseURL, "/")+"/oauth2/token")
	c.LocalProvision = localCredential("PROVISION")
	c.LocalRoleAssign = localCredential("ROLE_ASSIGN")
	c.LocalRoleRevoke = localCredential("ROLE_REVOKE")
	c.LocalBindingWrite = localCredential("BINDING_WRITE")
	c.LocalBindingDisable = localCredential("BINDING_DISABLE")
	c.FileGatewayApplicationID = os.Getenv("PORTAL_FILE_GATEWAY_APPLICATION_ID")
	c.ReportSourceApplicationID = os.Getenv("PORTAL_REPORT_SOURCE_APPLICATION_ID")
	return nil
}

// localCredential 读取精确职责凭据，不拼接业务身份或秘密。
// 参数：kind为固定配置用途。
// 返回值：门户自己的OAuth凭据。
func localCredential(kind string) LocalIdentityCredential {
	return LocalIdentityCredential{ClientID: os.Getenv("PORTAL_LOCAL_" + kind + "_CLIENT_ID"), ClientSecret: os.Getenv("PORTAL_LOCAL_" + kind + "_CLIENT_SECRET")}
}

// directoryMode 归一化直接构造Config的兼容默认。
// 参数：无。
// 返回值：显式模式；非法值由validate失败关闭。
func (c Config) directoryMode() localidentity.Mode {
	if c.LocalDirectoryMode == "" {
		return localidentity.ModeCRM
	}
	return localidentity.Mode(c.LocalDirectoryMode)
}

// validateLocalIdentityConfig 核验完整独立业务依赖且不要求CRM服务。
// 参数：c为固定本地模式配置。
// 返回值：缺少真实身份、文件或安全依赖时失败。
func validateLocalIdentityConfig(c Config) error {
	if len(c.LocalInviteHMACKey) < 32 {
		return fmt.Errorf("PORTAL_LOCAL_INVITE_HMAC_KEY_BASE64 must decode to at least 32 bytes")
	}
	for _, key := range [][]byte{c.EncryptionKey, c.ReportIngestDescriptorKey, c.ReportContentKey, c.HMACKey} {
		if bytes.Equal(c.LocalInviteHMACKey, key) {
			return fmt.Errorf("local invitation HMAC key must be independent")
		}
	}
	seen := map[string]bool{c.OIDCClientID: true, c.PlatformAuditClientID: true, c.FileGatewayClientID: true}
	for kind, value := range map[string]LocalIdentityCredential{"PROVISION": c.LocalProvision, "ROLE_ASSIGN": c.LocalRoleAssign, "ROLE_REVOKE": c.LocalRoleRevoke, "BINDING_WRITE": c.LocalBindingWrite, "BINDING_DISABLE": c.LocalBindingDisable} {
		if value.ClientID == "" || value.ClientSecret == "" || value.ClientID != strings.TrimSpace(value.ClientID) || value.ClientSecret != strings.TrimSpace(value.ClientSecret) || seen[value.ClientID] {
			return fmt.Errorf("PORTAL_LOCAL_%s_CLIENT_ID/SECRET must provide separate exact-scope credentials", kind)
		}
		seen[value.ClientID] = true
	}
	token, err := url.ParseRequestURI(c.LocalPlatformTokenURL)
	if err != nil || token.Host == "" || token.User != nil || token.RawQuery != "" || token.Fragment != "" || (token.Scheme != "http" && token.Scheme != "https") {
		return fmt.Errorf("PORTAL_LOCAL_PLATFORM_TOKEN_URL must be a valid OAuth endpoint")
	}
	if c.FileGatewayApplicationID == "" || c.FileGatewayApplicationID != strings.TrimSpace(c.FileGatewayApplicationID) || c.FileGatewayApplicationID != c.ReportSourceApplicationID || c.FileGatewayBaseURL == "" || c.FileGatewayLocalEnabled {
		return fmt.Errorf("local reports require HTTP file gateway with matching PORTAL_FILE_GATEWAY_APPLICATION_ID and PORTAL_REPORT_SOURCE_APPLICATION_ID")
	}
	if len(c.ReportContentKey) != 32 || c.ReportStoreRoot == "" || c.PDFConfigRoot == "" || c.CJKFontPath == "" {
		return fmt.Errorf("local reports require content key, private vault, PDF configuration and CJK font")
	}
	return validateLocalReportDownloadConfig(c)
}
