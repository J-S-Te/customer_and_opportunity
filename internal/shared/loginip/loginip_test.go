package loginip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFromRequestUsesManagedProxyAddress(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	req.RemoteAddr = "172.18.0.12:8080"
	req.Header.Set("X-Real-IP", "125.120.19.87")
	req.Header.Set("X-Forwarded-For", "8.8.8.8, 125.120.19.87")
	if got := FromRequest(req); got != "125.120.19.87" {
		t.Fatalf("FromRequest()=%q", got)
	}
}

func TestFromRequestRejectsSpoofedForwardingHeadersFromPublicPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	req.RemoteAddr = "203.0.113.5:12345"
	req.Header.Set("X-Real-IP", "8.8.8.8")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := FromRequest(req); got != "203.0.113.5" {
		t.Fatalf("FromRequest()=%q", got)
	}
}

func TestFromRequestOmitsPrivateOnlyAddresses(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	req.RemoteAddr = "172.18.0.12:8080"
	req.Header.Set("X-Real-IP", "172.18.0.1")
	req.Header.Set("X-Forwarded-For", "10.0.0.2, 172.18.0.1")
	if got := FromRequest(req); got != "" {
		t.Fatalf("FromRequest()=%q", got)
	}
}
