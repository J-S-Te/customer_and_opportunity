package portalbootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 连打 N+1 次：前 N 次进入 handler（创建 activation），第 N+1 次在中间件被 Abort，
// 既不创建 activation、也不写 auth event，符合"M-10 不产生副作用"的要求。
func TestPortalLoginRateLimitBlocksBurstPerClient(t *testing.T) {
	fixture := newAuthEntryFixture(t, Config{LoginRateLimit: 3})
	for attempt := 1; attempt <= 3; attempt++ {
		recorder := fixture.get("/customer-portal/auth/login")
		if recorder.Code != http.StatusFound {
			t.Fatalf("attempt %d status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if fixture.repository.createCalls != 3 {
		t.Fatalf("activation create calls = %d, want 3", fixture.repository.createCalls)
	}

	request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/login", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "PORTAL_RATE_LIMITED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("rate limited response must carry Retry-After")
	}
	if fixture.repository.createCalls != 3 {
		t.Fatalf("rate limited request created an activation: calls=%d", fixture.repository.createCalls)
	}
	if findCookie(t, recorder, loginStateCookieName) != nil {
		t.Fatal("rate limited request must not issue a login state cookie")
	}
}

func TestPortalLoginRateLimitIsolatesClients(t *testing.T) {
	fixture := newAuthEntryFixture(t, Config{LoginRateLimit: 1})
	send := func(remoteAddr string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/login", nil)
		request.RemoteAddr = remoteAddr
		recorder := httptest.NewRecorder()
		fixture.router.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := send("198.51.100.7:40001"); recorder.Code != http.StatusFound {
		t.Fatalf("first client status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := send("198.51.100.7:40002"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("same client second attempt status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 同一 IP 不同源端口仍是同一桶，另一地址必须独立计数。
	if recorder := send("198.51.100.8:40003"); recorder.Code != http.StatusFound {
		t.Fatalf("second client status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPortalRateLimitUsesTrustedProxyHeader(t *testing.T) {
	trusted := trustedPrefixes(t, "172.31.255.250/32")
	fixture := newAuthEntryFixture(t, Config{LoginRateLimit: 1, TrustedProxies: trusted})
	send := func(realIP string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/login", nil)
		request.RemoteAddr = "172.31.255.250:5000"
		request.Header.Set("X-Real-IP", realIP)
		recorder := httptest.NewRecorder()
		fixture.router.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := send("203.0.113.20"); recorder.Code != http.StatusFound {
		t.Fatalf("first client status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := send("203.0.113.20"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("same forwarded client status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 可信代理下不同真实客户端不能共用一个桶（否则一次突发即锁死全站）。
	if recorder := send("203.0.113.21"); recorder.Code != http.StatusFound {
		t.Fatalf("distinct forwarded client status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPortalRateLimitIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	fixture := newAuthEntryFixture(t, Config{LoginRateLimit: 1, TrustedProxies: trustedPrefixes(t, "172.31.255.250/32")})
	send := func(remoteAddr, realIP string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/login", nil)
		request.RemoteAddr = remoteAddr
		request.Header.Set("X-Real-IP", realIP)
		recorder := httptest.NewRecorder()
		fixture.router.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := send("10.0.0.9:5000", "203.0.113.20"); recorder.Code != http.StatusFound {
		t.Fatalf("first attempt status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 非可信对端轮换 X-Real-IP 不能绕过额度：桶键仍是直接对端地址。
	if recorder := send("10.0.0.9:5001", "203.0.113.99"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed forwarded header bypassed the limit: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPortalActivateRateLimitAppliesToInvitationEntry(t *testing.T) {
	fixture := newAuthEntryFixture(t, Config{ActivateRateLimit: 2})
	for attempt := 1; attempt <= 2; attempt++ {
		if recorder := fixture.get("/customer-portal/activate?token=invite-token"); recorder.Code != http.StatusFound {
			t.Fatalf("attempt %d status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := fixture.get("/customer-portal/activate?token=invite-token"); recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "PORTAL_RATE_LIMITED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.repository.createCalls != 2 {
		t.Fatalf("rate limited activate created an activation: calls=%d", fixture.repository.createCalls)
	}
}

func TestPortalCallbackRateLimitDoesNotConsumeActivation(t *testing.T) {
	fixture := newAuthEntryFixture(t, Config{CallbackRateLimit: 2})
	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		fixture.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/customer-portal/auth/callback?state=&code=code-a", nil))
		return recorder
	}
	for attempt := 1; attempt <= 2; attempt++ {
		// 无 state Cookie 时回调本来就会失败，但必须是在通过限流之后进入 handler。
		if recorder := request(); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := request(); recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "PORTAL_RATE_LIMITED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.repository.consumeCalls != 0 {
		t.Fatalf("callback consumed an activation: calls=%d", fixture.repository.consumeCalls)
	}
}

// 默认额度直接来自 Config 零值：忘记配置不能变成"完全不限流"。
func TestPortalRateLimitDefaultsKeepAnonymousEntriesBounded(t *testing.T) {
	config := Config{}
	if config.loginRateLimit() != defaultLoginRateLimit || config.activateRateLimit() != defaultActivateRateLimit || config.callbackRateLimit() != defaultCallbackRateLimit {
		t.Fatalf("default limits = %d/%d/%d", config.loginRateLimit(), config.activateRateLimit(), config.callbackRateLimit())
	}
	if config.authRateLimitWindow() != defaultAuthRateLimitWindow {
		t.Fatalf("default window = %s", config.authRateLimitWindow())
	}
}

func TestTrustedProxyCoverageWarningOnlySilencedByNonLoopbackPrefix(t *testing.T) {
	if hasNonLoopbackTrustedProxy(trustedPrefixes(t, "127.0.0.1/32", "::1/128")) {
		t.Fatal("loopback-only trusted proxies must keep the deployment warning")
	}
	if hasNonLoopbackTrustedProxy(nil) {
		t.Fatal("empty trusted proxies must keep the deployment warning")
	}
	if !hasNonLoopbackTrustedProxy(trustedPrefixes(t, "127.0.0.1/32", "172.31.255.250/32")) {
		t.Fatal("gateway address in trusted proxies must silence the warning")
	}
}
