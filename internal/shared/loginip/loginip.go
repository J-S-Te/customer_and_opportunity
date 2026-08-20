// Package loginip validates the optional user login IP received through the
// trusted base-platform authorization context. It must never inspect request
// forwarding headers or peer addresses.
package loginip

import (
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
