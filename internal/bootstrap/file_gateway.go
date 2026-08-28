package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// newAttachmentFileGatewayClient 构造 CRM 自己的最小权限机器客户端。Token 只在请求上下文内
// 获取，不写日志、不落库，也不与 Portal 的文件网关凭据复用。
func newAttachmentFileGatewayClient(config Config) (*filegatewayclient.Client, error) {
	if config.AttachmentFileGatewayMode == "legacy" {
		return nil, nil
	}
	tokenURL, err := url.ParseRequestURI(config.AttachmentFileGatewayTokenURL)
	if err != nil || tokenURL.Host == "" || tokenURL.User != nil || tokenURL.RawQuery != "" || tokenURL.Fragment != "" || tokenURL.Scheme != "http" && tokenURL.Scheme != "https" {
		return nil, errors.New("ATTACHMENT_FILE_GATEWAY_TOKEN_URL must be a valid HTTP(S) URL")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	oauthConfig := clientcredentials.Config{ClientID: config.AttachmentFileGatewayClientID, ClientSecret: config.AttachmentFileGatewaySecret, TokenURL: config.AttachmentFileGatewayTokenURL, Scopes: strings.Fields(config.AttachmentFileGatewayScope)}
	return filegatewayclient.New(config.AttachmentFileGatewayBaseURL, httpClient, func(ctx context.Context) (string, error) {
		token, tokenErr := oauthConfig.Token(context.WithValue(ctx, oauth2.HTTPClient, httpClient))
		if tokenErr != nil {
			return "", tokenErr
		}
		return token.AccessToken, nil
	})
}
