package loginip

import (
	"net/http/httptest"
	"testing"
)

// AUD-IP-2026：办公网/VPN/回环都是合法客户端来源，不能因"非公网"被丢弃。
func TestNormalizeAcceptsPrivateAndLoopbackAddresses(t *testing.T) {
	for _, value := range []string{"10.8.0.5", "192.168.1.20", "172.31.255.250", "127.0.0.1", "100.64.0.9", "2001:db8::1"} {
		if got := Normalize(value); got != value {
			t.Errorf("Normalize(%q) = %q，合法地址不应被丢弃", value, got)
		}
	}
	for _, value := range []string{"", "not-an-ip", "0.0.0.0", "::", "ff02::1"} {
		if got := Normalize(value); got != "" {
			t.Errorf("Normalize(%q) = %q，应返回空", value, got)
		}
	}
}

func TestClientOnlyTrustsConfiguredProxy(t *testing.T) {
	trusted, err := ParseTrusted("127.0.0.1/32,172.31.255.250/32")
	if err != nil {
		t.Fatalf("ParseTrusted: %v", err)
	}

	// 可信代理转发：采信 X-Real-IP。
	request := httptest.NewRequest("GET", "/api/v1/customers", nil)
	request.RemoteAddr = "172.31.255.250:51234"
	request.Header.Set("X-Real-IP", "203.0.113.10")
	if ip, source := Client(request, trusted); ip != "203.0.113.10" || source != "TRUSTED_PROXY" {
		t.Fatalf("Client() = %q/%q，want 203.0.113.10/TRUSTED_PROXY", ip, source)
	}

	// 同一私网段但不是登记代理：必须忽略转发头，用真实对端。
	request = httptest.NewRequest("GET", "/api/v1/customers", nil)
	request.RemoteAddr = "10.0.0.9:51234"
	request.Header.Set("X-Real-IP", "203.0.113.99")
	if ip, source := Client(request, trusted); ip != "10.0.0.9" || source != "DIRECT" {
		t.Fatalf("Client() = %q/%q，非可信对端的转发头必须被忽略", ip, source)
	}
}

func TestClientFallsBackToForwardedChainOnlyWhenTrusted(t *testing.T) {
	trusted, err := ParseTrusted("127.0.0.1")
	if err != nil {
		t.Fatalf("ParseTrusted: %v", err)
	}
	request := httptest.NewRequest("GET", "/api/v1/customers", nil)
	request.RemoteAddr = "127.0.0.1:5000"
	request.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.7")
	if ip, source := Client(request, trusted); ip != "203.0.113.7" || source != "TRUSTED_PROXY" {
		t.Fatalf("Client() = %q/%q，want 203.0.113.7/TRUSTED_PROXY", ip, source)
	}
}

func TestParseTrustedRejectsMalformedEntries(t *testing.T) {
	if _, err := ParseTrusted("127.0.0.1/32,not-an-ip"); err == nil {
		t.Fatal("非法可信代理配置必须报错，不能静默忽略")
	}
}
