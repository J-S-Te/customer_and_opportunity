package portalreportworker

import (
	"context"
	"net/http"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/reportsecurity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/integrationhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

func newFileIngestor(cfg Config, protector report.DescriptorProtector) (report.FileIngestor, *reportsecurity.EncryptedStore, error) {
	store, err := reportsecurity.NewEncryptedStore(cfg.ReportStoreRoot, cfg.ReportContentKey, cfg.TenantID, protector)
	if err != nil {
		return nil, nil, err
	}
	transport, err := integrationhttp.NewTransport(cfg.FileGatewayTLS, 5*time.Second)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	oauth := clientcredentials.Config{ClientID: cfg.FileGatewayClientID, ClientSecret: cfg.FileGatewayClientSecret, TokenURL: cfg.FileGatewayTokenURL, Scopes: []string{"platform:file:portal_report"}, AuthStyle: oauth2.AuthStyleInHeader}
	gateway, err := filegatewayclient.New(cfg.FileGatewayURL, client, func(ctx context.Context) (string, error) {
		token, err := oauth.Token(context.WithValue(ctx, oauth2.HTTPClient, client))
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	// newReportSource 使用 Portal 专用委托网关端点，
	// 绝不使用 Project 的广泛应用凭据。
	ingestor, err := reportsecurity.NewGatewayIngestor(gateway, store)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return ingestor, store, nil
}
