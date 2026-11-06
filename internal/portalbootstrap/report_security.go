package portalbootstrap

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/reportsecurity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"gorm.io/gorm"
)

type reportDownloadProviders struct {
	Store     *reportsecurity.EncryptedStore
	Risk      report.DownloadRiskPolicy
	Watermark report.DownloadWatermarker
}

// newReportDownloadProviders 保留默认CRM身份服务与原报告安全链。
// 参数：ctx/db/c/protector为既有门户装配依赖。
// 返回值：扫描、加密存储、风险和水印提供器；缺配置时不启用。
func newReportDownloadProviders(ctx context.Context, db *gorm.DB, c Config, protector report.DescriptorProtector) (*reportDownloadProviders, error) {
	if len(c.ReportContentKey) == 0 {
		return nil, nil
	}
	tls := integrationhttp.TLSOptions{}
	if err := tls.ValidateEndpoints(c.ReportIdentityURL, c.CRMInviteTokenURL); err != nil {
		return nil, err
	}
	transport, err := integrationhttp.NewTransport(tls, 5*time.Second)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	oauth := clientcredentials.Config{ClientID: c.ReportIdentityClientID, ClientSecret: c.ReportIdentityClientSecret, TokenURL: c.CRMInviteTokenURL, Scopes: []string{portalreportreference.Scope}, AuthStyle: oauth2.AuthStyleInHeader}
	identity := &reportsecurity.IdentityHTTPClient{Endpoint: c.ReportIdentityURL, Client: client, Token: func(requestContext context.Context) (string, error) {
		token, err := oauth.Token(context.WithValue(requestContext, oauth2.HTTPClient, client))
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	}}
	return newReportDownloadProvidersWithIdentity(ctx, db, c, protector, identity)
}

// newReportDownloadProvidersWithIdentity 显式替换身份权威，复用既有安全存储与下载链。
// 参数：ctx/db/c/protector为既有依赖，identity为固定模式的真实客户权威。
// 返回值：完整安全提供器；本地权威或安全配置缺失时失败关闭。
func newReportDownloadProvidersWithIdentity(ctx context.Context, db *gorm.DB, c Config, protector report.DescriptorProtector, identity reportsecurity.IdentitySource) (*reportDownloadProviders, error) {
	if len(c.ReportContentKey) == 0 {
		return nil, nil
	}
	if db == nil || protector == nil || identity == nil {
		return nil, reportsecurity.ErrUnavailable
	}
	store, err := reportsecurity.NewEncryptedStore(c.ReportStoreRoot, c.ReportContentKey, c.TenantID, protector)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			_ = store.Close()
		}
	}()
	watermark, err := reportsecurity.NewPDFWatermarker(c.PDFConfigRoot, c.CJKFontPath, identity, reportsecurity.GORMRequestNumbers{DB: db})
	if err != nil {
		return nil, err
	}
	risk, err := reportsecurity.NewAuditRiskPolicy(reportsecurity.GORMRiskEvidence{DB: db}, 5*time.Minute, 5)
	if err != nil {
		return nil, err
	}
	initialized = true
	return &reportDownloadProviders{Store: store, Risk: risk, Watermark: watermark}, nil
}

// validateLocalReportDownloadConfig 校验本地报告安全配置且不要求CRM身份URL。
// 参数：c为受控启动配置。
// 返回值：密钥、vault及PDF配置错误；不读取或放宽客户ACL。
func validateLocalReportDownloadConfig(c Config) error {
	configured := len(c.ReportContentKey) > 0 || c.ReportStoreRoot != ""
	if !configured {
		return nil
	}
	if len(c.ReportContentKey) != 32 || c.ReportStoreRoot == "" || c.PDFConfigRoot == "" || c.CJKFontPath == "" || !strings.HasPrefix(c.ReportStoreRoot, "/") || string(c.ReportContentKey) == string(c.ReportIngestDescriptorKey) || string(c.ReportContentKey) == string(c.EncryptionKey) {
		return reportsecurity.ErrInvalid
	}
	return nil
}

// validateReportDownloadConfig 保留CRM默认报告依赖校验。
// 参数：c为既有启动配置。
// 返回值：缺失、混用密钥或端点错误。
func validateReportDownloadConfig(c Config) error {
	configured := len(c.ReportContentKey) > 0 || c.ReportStoreRoot != "" || c.ReportIdentityURL != "" || c.ReportIdentityClientID != "" || c.ReportIdentityClientSecret != ""
	if !configured {
		return nil
	}
	if len(c.ReportContentKey) != 32 || c.ReportStoreRoot == "" || c.ReportIdentityURL == "" || c.ReportIdentityClientID == "" || c.ReportIdentityClientSecret == "" || c.PDFConfigRoot == "" || c.CJKFontPath == "" {
		return reportsecurity.ErrInvalid
	}
	if string(c.ReportContentKey) == string(c.ReportIngestDescriptorKey) || string(c.ReportContentKey) == string(c.EncryptionKey) {
		return reportsecurity.ErrInvalid
	}
	if !strings.HasPrefix(c.ReportStoreRoot, "/") {
		return reportsecurity.ErrInvalid
	}
	return (integrationhttp.TLSOptions{}).ValidateEndpoints(c.ReportIdentityURL, c.CRMInviteTokenURL)
}
