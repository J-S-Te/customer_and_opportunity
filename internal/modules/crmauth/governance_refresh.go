package crmauth

import (
	"context"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"strings"
	"time"
)

var errRefreshRejected = errors.New("CRM refresh token is no longer valid")

// RefreshToken 使用服务器持有的刷新令牌轮换，调用方必须持有会话数据库锁。
// 参数：ctx为有界请求，refresh为服务器解密的原始刷新令牌。
// 返回值：轮换后材料及到期时间；明确拒绝返回专用错误，临时失败返回依赖不可用。
func (c *platformOIDCClient) RefreshToken(ctx context.Context, refresh string) (tokenRefreshResult, error) {
	if strings.TrimSpace(refresh) == "" {
		return tokenRefreshResult{}, errRefreshRejected
	}
	seed := &oauth2.Token{RefreshToken: refresh, Expiry: time.Now().Add(-time.Minute)}
	token, err := c.config.TokenSource(oidc.ClientContext(ctx, c.httpClient), seed).Token()
	if err != nil {
		var protocol *oauth2.RetrieveError
		if errors.As(err, &protocol) && (protocol.ErrorCode == "invalid_grant" || protocol.ErrorCode == "invalid_token") {
			return tokenRefreshResult{}, errRefreshRejected
		}
		return tokenRefreshResult{}, ErrAuthorizationUnavailable
	}
	if token.AccessToken == "" || !token.Expiry.After(time.Now()) {
		return tokenRefreshResult{}, ErrAuthorizationUnavailable
	}
	rotated := token.RefreshToken
	if rotated == "" {
		rotated = refresh
	}
	return tokenRefreshResult{AccessToken: token.AccessToken, RefreshToken: rotated, ExpiresAt: token.Expiry.UTC()}, nil
}
