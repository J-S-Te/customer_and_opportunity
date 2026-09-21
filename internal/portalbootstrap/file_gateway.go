package portalbootstrap

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

func newPortalFileGatewayClient(config Config) (*filegatewayclient.Client, error) {
	if strings.TrimSpace(config.FileGatewayBaseURL) == "" {
		return nil, nil
	}
	httpClient := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	oauth := clientcredentials.Config{
		ClientID: config.FileGatewayClientID, ClientSecret: config.FileGatewayClientSecret,
		TokenURL: strings.TrimRight(config.PlatformBaseURL, "/") + "/oauth2/token", Scopes: strings.Fields(config.FileGatewayScope),
	}
	return filegatewayclient.New(config.FileGatewayBaseURL, httpClient, func(ctx context.Context) (string, error) {
		token, err := oauth.Token(context.WithValue(ctx, oauth2.HTTPClient, httpClient))
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	})
}
