package portalbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

// authEntryRepository 是登录入口测试的账号仓储替身：只记录 activation 的创建/消费次数，
// 使限流与 state 绑定断言能证明"没有进入业务副作用"，而不依赖真实数据库。
type authEntryRepository struct {
	reportNotificationRouteAccountRepository
	createCalls  int
	consumeCalls int
	activations  []account.ActivationContext
}

func (r *authEntryRepository) CreateActivation(_ context.Context, value *account.ActivationContext) error {
	r.createCalls++
	r.activations = append(r.activations, *value)
	return nil
}

// ConsumeActivation 有意直接失败：state 校验通过的路径已经有 consumeCalls 作为证据，
// 不需要伪造 nonce/PKCE 密文去走完整个令牌交换。
func (r *authEntryRepository) ConsumeActivation(context.Context, string, time.Time) (*account.ActivationContext, error) {
	r.consumeCalls++
	return nil, account.ErrInvalidLoginState
}

// authEntryOIDC 记录服务端签发的 state，便于断言下发的 Cookie 与服务端状态一致。
type authEntryOIDC struct{ state string }

func (o *authEntryOIDC) AuthorizationURL(state, _, _, _ string) (string, error) {
	o.state = state
	return "https://sso.example/oauth2/authorize?state=" + state, nil
}

func (o *authEntryOIDC) ExchangeAndValidate(context.Context, string, string, string) (account.Claims, error) {
	return account.Claims{}, nil
}

func (o *authEntryOIDC) UserInfo(context.Context, string) (account.Claims, error) {
	return account.Claims{}, nil
}

type authEntryInvites struct {
	subject  string
	customer uint64
}

func (i authEntryInvites) Verify(context.Context, string) (account.VerifiedInvite, error) {
	return account.VerifiedInvite{TenantID: "tenant-a", ExpectedPlatformUserID: i.subject, CustomerID: i.customer}, nil
}

func (authEntryInvites) Consume(context.Context, string, string) error { return nil }

// authEntryProtector 使用恒等变换：本测试只关心 state Cookie，不验证密文强度。
type authEntryProtector struct{}

func (authEntryProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) { return value, nil }
func (authEntryProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) { return value, nil }

type authEntryFixture struct {
	router     *gin.Engine
	repository *authEntryRepository
	oidc       *authEntryOIDC
}

func newAuthEntryFixture(t *testing.T, config Config) *authEntryFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if config.PathPrefix == "" {
		config.PathPrefix = "/customer-portal"
	}
	if config.TenantID == "" {
		config.TenantID = "tenant-a"
	}
	if config.SessionCookieName == "" {
		config.SessionCookieName = "portal_session"
	}
	if config.PublicOrigin == "" {
		config.PublicOrigin = "https://portal.example"
	}
	if len(config.HMACKey) == 0 {
		config.HMACKey = []byte(strings.Repeat("h", 32))
	}
	repository := &authEntryRepository{reportNotificationRouteAccountRepository: reportNotificationRouteAccountRepository{
		link: &account.IdentityLink{Model: database.Model{TenantID: "tenant-a"}, PlatformUserID: "invite-subject", CustomerID: 7, Status: account.IdentityActive},
	}}
	oidc := &authEntryOIDC{}
	invites := authEntryInvites{subject: "invite-subject", customer: 7}
	service := account.NewService(repository, oidc, invites, authEntryProtector{}, account.SystemClock{}, account.CryptoRandom{}, "sha256:catalog", time.Hour)
	return &authEntryFixture{
		router:     NewRouter(RouterDependencies{Config: config, Account: service}),
		repository: repository,
		oidc:       oidc,
	}
}

func (f *authEntryFixture) get(path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func findCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func TestPortalLoginStateCookieAttributes(t *testing.T) {
	expires := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		config Config
		path   string
		secure bool
	}{
		{name: "生产同源安全前缀", config: Config{PathPrefix: "/customer-portal", SessionCookieSecure: true}, path: "/customer-portal/auth", secure: true},
		{name: "本地非 Secure 会话", config: Config{PathPrefix: "/customer-portal", SessionCookieSecure: false}, path: "/customer-portal/auth", secure: false},
		{name: "自定义路径前缀", config: Config{PathPrefix: "/portal-root", SessionCookieSecure: true}, path: "/portal-root/auth", secure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cookie := loginStateCookie(test.config, "state-value", expires)
			if cookie.Name != loginStateCookieName || cookie.Value != "state-value" {
				t.Fatalf("cookie name/value = %q/%q", cookie.Name, cookie.Value)
			}
			// 路径必须收窄到 <prefix>/auth：Spine 页面、/activate 与业务 API 都不应收到 state。
			if cookie.Path != test.path {
				t.Fatalf("cookie path = %q, want %q", cookie.Path, test.path)
			}
			if !cookie.HttpOnly || cookie.Secure != test.secure || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie flags = %#v", cookie)
			}
			// 未设置 Domain：state 只属于当前主机，避免被同父域的子域读取。
			if cookie.Domain != "" {
				t.Fatalf("cookie domain must be empty, got %q", cookie.Domain)
			}
			if !cookie.Expires.Equal(expires) || cookie.MaxAge != 0 {
				t.Fatalf("cookie expiry = %v maxAge=%d", cookie.Expires, cookie.MaxAge)
			}
		})
	}
}

func TestPortalExpiredLoginStateCookieClearsSameScope(t *testing.T) {
	config := Config{PathPrefix: "/customer-portal", SessionCookieSecure: true}
	cookie := expiredLoginStateCookie(config)
	issued := loginStateCookie(config, "state-value", time.Now().Add(loginStateTTL))
	if cookie.Name != issued.Name || cookie.Path != issued.Path || cookie.SameSite != issued.SameSite {
		t.Fatalf("clearing cookie must match issued scope: %#v vs %#v", cookie, issued)
	}
	if cookie.MaxAge != -1 || cookie.Value != "" || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("clearing cookie = %#v", cookie)
	}
}

func TestVerifyLoginState(t *testing.T) {
	for _, test := range []struct {
		name        string
		browser     string
		query       string
		expectValid bool
	}{
		{name: "相等", browser: "s-abc_123-XYZ", query: "s-abc_123-XYZ", expectValid: true},
		{name: "缺少 Cookie", browser: "", query: "s-abc", expectValid: false},
		{name: "缺少查询 state", browser: "s-abc", query: "", expectValid: false},
		{name: "两侧都缺失", browser: "", query: "", expectValid: false},
		{name: "不相等", browser: "s-abc", query: "s-abd", expectValid: false},
		{name: "前缀相同但更长", browser: "s-abc", query: "s-abc-extra", expectValid: false},
		{name: "相等但含换行", browser: "s-abc\r\nInjected: 1", query: "s-abc\r\nInjected: 1", expectValid: false},
		{name: "仅查询侧含换行", browser: "s-abc", query: "s-abc\n", expectValid: false},
		{name: "两侧相同含制表符", browser: "s\tabc", query: "s\tabc", expectValid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyLoginState(test.browser, test.query)
			if test.expectValid {
				if err != nil {
					t.Fatalf("verifyLoginState() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid state accepted")
			}
			// 复用既有错误契约（401 PORTAL_OIDC_INVALID_STATE），不引入前端未处理的新错误码。
			if appErr := apperror.As(err); appErr.Code != "PORTAL_OIDC_INVALID_STATE" || appErr.HTTPStatus != http.StatusUnauthorized {
				t.Fatalf("error = %s/%d", appErr.Code, appErr.HTTPStatus)
			}
		})
	}
}

func TestPortalLoginEntriesBindStateToBrowserCookie(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "普通登录入口", path: "/customer-portal/auth/login"},
		{name: "邀请激活入口", path: "/customer-portal/activate?token=invite-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthEntryFixture(t, Config{})
			recorder := fixture.get(test.path)
			if recorder.Code != http.StatusFound {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if fixture.repository.createCalls != 1 {
				t.Fatalf("activation create calls = %d, want 1", fixture.repository.createCalls)
			}
			cookie := findCookie(t, recorder, loginStateCookieName)
			if cookie == nil {
				t.Fatal("begin entry did not set the login state cookie")
			}
			issued := fixture.oidc.state
			if issued == "" || cookie.Value != issued {
				t.Fatalf("cookie value = %q, redirect state = %q", cookie.Value, issued)
			}
			if cookie.Path != "/customer-portal/auth" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie scope = %#v", cookie)
			}
			// 服务端仍只持久化摘要：Cookie 里的原始 state 不得以明文入库。
			activation := fixture.repository.activations[0]
			if activation.StateHash == issued || activation.StateHash != base64.RawURLEncoding.EncodeToString(sha256Bytes(issued)) {
				t.Fatalf("activation state hash must be the opaque digest")
			}
		})
	}
}

func TestPortalCallbackRequiresBrowserBoundState(t *testing.T) {
	for _, test := range []struct {
		name         string
		browserState string
		queryState   string
		wantConsumed int
	}{
		{name: "没有绑定 Cookie", browserState: "", queryState: "state-a", wantConsumed: 0},
		{name: "Cookie 与 state 不同", browserState: "state-a", queryState: "state-b", wantConsumed: 0},
		{name: "查询未带 state", browserState: "state-a", queryState: "", wantConsumed: 0},
		{name: "Cookie 与 state 一致", browserState: "state-a", queryState: "state-a", wantConsumed: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthEntryFixture(t, Config{})
			request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/callback?code=code-a&state="+test.queryState, nil)
			if test.browserState != "" {
				request.AddCookie(&http.Cookie{Name: loginStateCookieName, Value: test.browserState})
			}
			recorder := httptest.NewRecorder()
			fixture.router.ServeHTTP(recorder, request)

			if fixture.repository.consumeCalls != test.wantConsumed {
				t.Fatalf("activation consume calls = %d, want %d", fixture.repository.consumeCalls, test.wantConsumed)
			}
			if test.wantConsumed == 0 {
				// 校验失败必须停在激活消费之前，并保持既有 401 错误契约。
				if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "PORTAL_OIDC_INVALID_STATE") {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
			}
			// 成功和失败都必须清理 state Cookie，保证一次登录只允许一次回调。
			cleared := findCookie(t, recorder, loginStateCookieName)
			if cleared == nil || cleared.MaxAge != -1 || cleared.Path != "/customer-portal/auth" || cleared.Value != "" {
				t.Fatalf("login state cookie was not cleared: %#v", cleared)
			}
		})
	}
}

func TestPortalCallbackOIDCErrorAlsoClearsStateCookie(t *testing.T) {
	fixture := newAuthEntryFixture(t, Config{})
	request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/callback?error=access_denied", nil)
	request.AddCookie(&http.Cookie{Name: loginStateCookieName, Value: "state-a"})
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.repository.consumeCalls != 0 {
		t.Fatal("OIDC error callback must not consume the activation")
	}
	if cleared := findCookie(t, recorder, loginStateCookieName); cleared == nil || cleared.MaxAge != -1 {
		t.Fatalf("login state cookie was not cleared: %#v", cleared)
	}
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func trustedPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}
