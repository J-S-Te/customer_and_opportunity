package portalbootstrap

import (
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// ErrPortalRateLimited 是登录入口限流的稳定错误契约，前端与网关共用 429 语义。
var ErrPortalRateLimited = apperror.New(http.StatusTooManyRequests, "PORTAL_RATE_LIMITED", "too many login attempts, retry later")

// portalAuthRateLimit 按客户端地址为匿名登录入口做进程内固定窗口限流（M-10）。
//
// 语义与限制（与平台 FixedWindowRateLimit 保持一致）：
//   - 桶键取 loginip.Client 的结果，只有直接对端命中 Config.TrustedProxies 时才采信
//     X-Real-IP/X-Forwarded-For；未配置可信代理时键会退化为网关地址，所有客户端共用
//     一个桶——因此上线时必须同时配置 APP_TRUSTED_PROXIES；
//   - 桶为进程内 map，只保证单副本额度。多副本部署总额度约为 N 倍，必须由入口网关
//     limit_req 兜底（见 frontend/nginx/default.conf 的 portal_auth 区）；
//   - 请求在进入 handler 之前被 Abort，因此被限流的请求不会创建 activation 上下文、
//     不写 portal_auth_events，也不发起 OIDC 授权跳转。
func portalAuthRateLimit(config Config, limit int) gin.HandlerFunc {
	if limit <= 0 {
		limit = defaultLoginRateLimit
	}
	window := config.authRateLimitWindow()
	clientKey := portalRateLimitClientKey(config.TrustedProxies)
	return fixedWindowRateLimit(limit, window, clientKey)
}

// portalRateLimitClientKey 返回稳定、可用于日志关联的桶键。空地址统一落到 unknown，
// 避免未采集地址的请求绕过额度。
func portalRateLimitClientKey(trusted []netip.Prefix) func(*http.Request) string {
	return func(request *http.Request) string {
		client, _ := loginip.Client(request, trusted)
		if client == "" {
			return "unknown"
		}
		return client
	}
}

// fixedWindowRateLimit 是固定窗口计数器：窗口起点由该键的第一次请求建立，
// 跨窗口即重置。互斥锁同时保护计数与清理，清理频率最多每窗口一次。
func fixedWindowRateLimit(limit int, window time.Duration, key func(*http.Request) string) gin.HandlerFunc {
	type bucket struct {
		startedAt time.Time
		count     int
	}
	var mutex sync.Mutex
	buckets := make(map[string]bucket)
	lastCleanup := time.Now()
	return func(c *gin.Context) {
		now := time.Now()
		bucketKey := key(c.Request)
		mutex.Lock()
		if now.Sub(lastCleanup) >= window {
			for candidate, current := range buckets {
				if now.Sub(current.startedAt) >= window {
					delete(buckets, candidate)
				}
			}
			lastCleanup = now
		}
		current := buckets[bucketKey]
		if current.startedAt.IsZero() || now.Sub(current.startedAt) >= window {
			current = bucket{startedAt: now}
		}
		current.count++
		buckets[bucketKey] = current
		allowed := current.count <= limit
		retryAfter := window - now.Sub(current.startedAt)
		mutex.Unlock()
		if !allowed {
			// Retry-After 帮助客户端和网关正确退避，不暴露任何额度或键细节。
			c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			response.Error(c, ErrPortalRateLimited)
			c.Abort()
			return
		}
		c.Next()
	}
}

// hasNonLoopbackTrustedProxy 报告可信代理名单是否覆盖了非回环对端。仅信任回环时，
// 网关转发的全部请求对端都是网关地址，桶键退化成一个，限流额度变成全站共享，
// 因此启动日志必须显式告警。
func hasNonLoopbackTrustedProxy(trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if !prefix.Addr().IsLoopback() {
			return true
		}
	}
	return false
}
