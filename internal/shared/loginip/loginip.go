// Package loginip 校验来自可信基础平台授权上下文，
// 或 OIDC 回调边界受管反向代理的
// 用户登录地址。
package loginip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// legacyPublicOnly 保留旧登录回调口径：只接受公网单播地址。
//
// 该过滤只服务于尚未迁移的 FromRequest 路径；新代码应使用 Client（允许办公网/VPN/回环，
// 并按显式可信代理判定来源）。登录回调迁移到 Client 后即可删除本函数。
func legacyPublicOnly(value string) string {
	parsed, err := netip.ParseAddr(value)
	if err != nil || !parsed.IsGlobalUnicast() || parsed.IsPrivate() {
		return ""
	}
	return parsed.String()
}

// Normalize 返回规范化后的单播地址；无效、未指定或组播地址返回空。
//
// AUD-IP-2026：办公网（10/8、192.168/16）、VPN（如 100.64/10）与回环地址都是
// 合法的客户端来源，不能因为"不是公网"就丢弃——丢弃会让审计显示"未采集"，
// 排障时反而丢失真实信息。地址是否可信由"从哪个入口观察到"决定，不由地址段决定。
func Normalize(value string) string {
	parsed, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !parsed.IsValid() || parsed.IsUnspecified() || parsed.IsMulticast() {
		return ""
	}
	return parsed.String()
}

// Client 解析本次业务操作的客户端地址与来源标记。
//
// 只有直接对端命中显式配置的可信代理（trusted，如前端网关的精确地址）时才采信
// X-Real-IP / X-Forwarded-For；否则一律以直接对端作为客户端地址，防止任意来源
// 用转发头伪造审计地址。返回的来源标记与平台协议一致：TRUSTED_PROXY / DIRECT。
func Client(r *http.Request, trusted []netip.Prefix) (string, string) {
	if r == nil {
		return "", "UNKNOWN"
	}
	peer := normalizePeer(r.RemoteAddr)
	if len(trusted) > 0 && peerTrusted(peer, trusted) {
		if ip := Normalize(r.Header.Get("X-Real-IP")); ip != "" {
			return ip, "TRUSTED_PROXY"
		}
		values := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(values) - 1; i >= 0; i-- {
			ip := Normalize(values[i])
			if ip == "" {
				continue
			}
			// 从右往左跳过已在可信代理名单里的地址：最先出现的非代理地址才是客户端。
			if address, err := netip.ParseAddr(ip); err == nil && peerTrusted(ip, trusted) {
				_ = address
				continue
			}
			return ip, "TRUSTED_PROXY"
		}
	}
	if peer != "" {
		return peer, "DIRECT"
	}
	return "", "UNKNOWN"
}

// ParseTrusted 解析逗号分隔的可信代理地址或 CIDR；非法项直接返回错误，
// 避免把拼写错误的代理配置静默忽略后仍然启动。
func ParseTrusted(value string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		address, err := netip.ParseAddr(item)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
	}
	return prefixes, nil
}

func normalizePeer(remoteAddr string) string {
	value := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return Normalize(strings.Trim(value, "[]"))
}

func peerTrusted(peer string, trusted []netip.Prefix) bool {
	address, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	for _, prefix := range trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// FromRequest 返回请求的公开客户端地址。仅当
// 直接对端为回环、私有或链路本地地址时才信任转发头，
// 这对应受管反向代理的部署边界。
// 公网直接对端不能通过 XFF 伪造自身地址。
func FromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if trustedProxyPeer(r.RemoteAddr) {
		if ip := legacyPublicOnly(Normalize(r.Header.Get("X-Real-IP"))); ip != "" {
			return ip
		}
		// 优先使用最右侧的公网条目。受管代理将观察到的
		// 对端追加于此，客户端提供的条目保留在其左侧。
		values := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(values) - 1; i >= 0; i-- {
			if ip := legacyPublicOnly(Normalize(values[i])); ip != "" {
				return ip
			}
		}
	}
	// 旧口径：对端不是代理时，对端地址是公网才记录；私网对端（容器/网关）返回空，
	// 避免把基础设施地址写成用户登录地址。新代码请改用 Client。
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	return legacyPublicOnly(Normalize(remote))
}

func trustedProxyPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
