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

func newReportDownloadProviders(ctx context.Context, db *gorm.DB, c Config, protector report.DescriptorProtector) (*reportDownloadProviders, error) {
	if len(c.ReportContentKey) == 0 {
		return nil, nil
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
