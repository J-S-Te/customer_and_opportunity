// Package loginip validates user login addresses received either from the
// trusted base-platform authorization context or from the managed reverse
// proxy at the OIDC callback boundary.
package loginip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Normalize returns a canonical public unicast address, or "" when the
// upstream session has no usable address. Private and special-use addresses
// are deliberately omitted so an internal proxy address cannot be promoted
// into a user-login audit field.
func Normalize(value string) string {
	parsed, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !parsed.IsValid() || !parsed.IsGlobalUnicast() || parsed.IsPrivate() {
		return ""
	}
	return parsed.String()
}

// FromRequest returns the public client address from a request. Forwarding
// headers are honoured only when the direct peer is a loopback/private/link-
// local address, which is the deployment boundary used by the managed reverse
// proxy. A publicly reachable direct peer cannot spoof its address with XFF.
func FromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if trustedProxyPeer(r.RemoteAddr) {
		if ip := Normalize(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		// Prefer the right-most public entry. The managed proxy appends its
		// observed peer there, while client-supplied entries remain to its left.
		values := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(values) - 1; i >= 0; i-- {
			if ip := Normalize(values[i]); ip != "" {
				return ip
			}
		}
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	return Normalize(remote)
}

func trustedProxyPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
