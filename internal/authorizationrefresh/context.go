// Package authorizationrefresh 只表达受认证自描述读对新鲜授权的要求。
package authorizationrefresh

import (
	"context"
	"net/http"
	"strings"
)

const Header = "X-Authorization-Refresh"

type refreshKey struct{}

// WithForce 将服务器内部的新鲜授权要求写入请求上下文。
// 参数：ctx为已认证调用链的上下文，不承载任何浏览器授权声明。
// 返回值：标记为强制复核的上下文。
func WithForce(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshKey{}, true)
}

// Forced 读取当前调用是否必须重新取得授权事实。
// 参数：ctx为请求上下文。
// 返回值：仅服务器设置了刷新标记时为真。
func Forced(ctx context.Context) bool {
	forced, _ := ctx.Value(refreshKey{}).(bool)
	return forced
}

// FromRequest 仅将准确自描述GET端点的单值刷新头转为内部标记。
// 参数：ctx为请求上下文，request为当前请求，允许已有公开应用前缀。
// 返回值：合法刷新请求返回新上下文，其他请求保持原上下文。
func FromRequest(ctx context.Context, request *http.Request) context.Context {
	if request == nil || request.URL == nil || request.Method != http.MethodGet {
		return ctx
	}
	values := request.Header.Values(Header)
	if len(values) != 1 || values[0] != "1" {
		return ctx
	}
	route := request.URL.Path
	for _, suffix := range []string{"/auth/me", "/auth/session", "/oauth2/authorization-context"} {
		if strings.HasSuffix(route, suffix) {
			return WithForce(ctx)
		}
	}
	return ctx
}
