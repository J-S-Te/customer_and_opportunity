package portalbootstrap

import (
	"context"
	"errors"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"golang.org/x/oauth2"
)

// RefreshToken 在门户数据库行锁内轮换服务器令牌，不改变客户角色或原截止。
// 参数：ctx为有界请求，refresh为解密后的服务器刷新令牌。
// 返回值：新访问及刷新材料；明确拒绝为失效，其他异常保持503语义且不泄漏响应正文。
func (a *OIDCAdapter) RefreshToken(ctx context.Context, refresh string) (account.RefreshedTokens, error) {
	if refresh == "" {
		return account.RefreshedTokens{}, account.ErrInvalidLoginState
	}
	token, err := a.config.TokenSource(oidc.ClientContext(ctx, a.httpClient), &oauth2.Token{RefreshToken: refresh, Expiry: time.Now().Add(-time.Minute)}).Token()
	if err != nil {
		var protocol *oauth2.RetrieveError
		if errors.As(err, &protocol) && (protocol.ErrorCode == "invalid_grant" || protocol.ErrorCode == "invalid_token") {
			return account.RefreshedTokens{}, account.ErrInvalidLoginState
		}
		return account.RefreshedTokens{}, account.ErrAuthorizationUnavailable
	}
	if token.AccessToken == "" || !token.Expiry.After(time.Now().UTC()) {
		return account.RefreshedTokens{}, account.ErrAuthorizationUnavailable
	}
	rotated := token.RefreshToken
	if rotated == "" {
		rotated = refresh
	}
	return account.RefreshedTokens{AccessToken: token.AccessToken, RefreshToken: rotated, ExpiresAt: token.Expiry.UTC()}, nil
}
