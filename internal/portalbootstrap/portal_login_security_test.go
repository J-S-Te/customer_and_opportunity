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
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

// authEntryRepository 是登录入口测试的账号仓储替身：只记录 activation 的创建/消费次数，
// 使限流与 state 绑定断言能证明"没有进入业务副作用"，而不依赖真实数据库。
type authEntryRepository struct {
	reportNotificationRouteAccountRepository
	createCalls  int
	consumeCalls int
	activations  []account.ActivationContext
}

// CreateActivation 用途：记录真实账号服务发出的激活写入。
// 参数：忽略请求上下文；value 为合成登录激活事务。
// 返回值：nil；仅保存夹具记录，不访问数据库。
func (r *authEntryRepository) CreateActivation(_ context.Context, value *account.ActivationContext) error {
	r.createCalls++
	r.activations = append(r.activations, *value)
	return nil
}

// ConsumeActivation 用途：记录回调消费尝试并拒绝继续令牌交换。
// 参数：忽略请求、状态摘要和消费时间；仅验证消费前安全闸门。
// 返回值：无上下文；返回非法登录状态。
func (r *authEntryRepository) ConsumeActivation(context.Context, string, time.Time) (*account.ActivationContext, error) {
	r.consumeCalls++
	return nil, account.ErrInvalidLoginState
}

// authEntryOIDC 记录服务端签发的 state，便于断言下发的 Cookie 与服务端状态一致。
type authEntryOIDC struct{ state string }

// AuthorizationURL 用途：记录真实服务生成的 state 并生成合成提供方地址。
// 参数：state 为随机状态；其余 nonce、PKCE 和回跳参数不参与断言。
// 返回值：测试授权地址及 nil 错误。
func (o *authEntryOIDC) AuthorizationURL(state, _, _, _ string) (string, error) {
	o.state = state
	return "https://sso.example/oauth2/authorize?state=" + state, nil
}

// ExchangeAndValidate 用途：提供未被本组入口测试调用的 OIDC 交换端口。
// 参数：忽略请求、授权码、PKCE 和 nonce；测试在消费阶段终止。
// 返回值：空测试声明及 nil 错误，不表示真实认证通过。
func (o *authEntryOIDC) ExchangeAndValidate(context.Context, string, string, string) (account.Claims, error) {
	return account.Claims{}, nil
}

// UserInfo 用途：提供本组入口测试不使用的用户信息端口。
// 参数：忽略请求上下文与合成访问令牌。
// 返回值：空测试声明及 nil 错误，不访问提供方。
func (o *authEntryOIDC) UserInfo(context.Context, string) (account.Claims, error) {
	return account.Claims{}, nil
}

type authEntryInvites struct {
	subject  string
	customer uint64
}

// Verify 用途：返回预设的合成邀请归属以测试激活入口。
// 参数：忽略请求和合成邀请令牌。
// 返回值：夹具指定的租户、用户与客户归属及 nil 错误。
func (i authEntryInvites) Verify(context.Context, string) (account.VerifiedInvite, error) {
	return account.VerifiedInvite{TenantID: "tenant-a", ExpectedPlatformUserID: i.subject, CustomerID: i.customer}, nil
}

// Consume 用途：提供入口测试不使用的邀请消费端口。
// 参数：忽略请求、合成邀请令牌及主体。
// 返回值：nil；不代表真实邀请已消费。
func (authEntryInvites) Consume(context.Context, string, string) error { return nil }

// authEntryProtector 使用恒等变换：本测试只关心 state Cookie，不验证密文强度。
type authEntryProtector struct{}

// Encrypt 用途：在 Cookie 入口测试中使用恒等密文替身。
// 参数：忽略请求；value 为测试保护材料。
// 返回值：原始字节及 nil 错误，不验证加密强度。
func (authEntryProtector) Encrypt(_ context.Context, value []byte) ([]byte, error) { return value, nil }

// Decrypt 用途：在 Cookie 入口测试中使用恒等解密替身。
// 参数：忽略请求；value 为测试保护材料。
// 返回值：原始字节及 nil 错误，不验证加密强度。
func (authEntryProtector) Decrypt(_ context.Context, value []byte) ([]byte, error) { return value, nil }

type authEntryFixture struct {
	router     *gin.Engine
	repository *authEntryRepository
	oidc       *authEntryOIDC
}

// newAuthEntryFixture 用途：装配真实账号服务和路由及无网络依赖的入口夹具。
// 参数：t 为测试上下文；config 为需验证的入口配置。
// 返回值：路由、账号仓储记录器及 OIDC 状态记录器。
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

// get 用途：通过真实 Portal 路由执行合成 GET 请求。
// 参数：path 为含查询参数的 Portal 请求路径。
// 返回值：HTTP 响应记录器。
func (f *authEntryFixture) get(path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

// findCookie 用途：从入口响应中查找指定 Cookie 以核验绑定或清理。
// 参数：t 为测试上下文；recorder 为响应；name 为 Cookie 名。
// 返回值：匹配 Cookie；未签发时 nil。
func findCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

// TestPortalLoginStateCookieAttributes 用途：验证登录 state Cookie 的路径和安全属性。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// TestPortalExpiredLoginStateCookieClearsSameScope 用途：验证清理 Cookie 与签发 Cookie 的作用域一致。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// TestVerifyLoginState 用途：验证浏览器 state 的缺失、失配及非法输入拒绝。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// TestPortalLoginEntriesBindStateToBrowserCookie 用途：验证普通与邀请入口都签发匹配的浏览器 state。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// TestPortalCallbackRequiresBrowserBoundState 用途：验证回调消费 activation 前核验并清理浏览器 state。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// TestPortalCallbackOIDCErrorAlsoClearsStateCookie 用途：验证提供方错误回调不消费 activation 且清理 state。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
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

// sha256Bytes 用途：计算 state 摘要以核对明文未持久化。
// 参数：value 为浏览器收到的原始测试 state。
// 返回值：SHA-256 摘要字节。
func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

// trustedPrefixes 用途：解析测试使用的受信代理地址范围。
// 参数：t 为测试上下文；values 为合成 CIDR 清单。
// 返回值：受信代理前缀切片；非法 CIDR 使测试失败。
func trustedPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}

// TestPortalCentralCallbackFailureClearsBrowserState 用途：验证中央回调提前失败也清理浏览器 state。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
func TestPortalCentralCallbackFailureClearsBrowserState(t *testing.T) {
	for _, test := range []struct {
		name         string
		centralState string
		query        string
	}{
		{name: "缺失中央 Cookie", query: "state=state-a&code=code-a"},
		{name: "中央 Cookie 失配", centralState: "state-b", query: "state=state-a&code=code-a"},
		{name: "桥接票据失败", centralState: "state-a", query: "state=state-a&session_link_ticket=ticket-a"},
		{name: "桥接同时带授权码", centralState: "state-a", query: "state=state-a&session_link_ticket=ticket-a&code=code-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthEntryFixture(t, Config{CentralSessionEnabled: true})
			request := httptest.NewRequest(http.MethodGet, "/customer-portal/auth/callback?"+test.query, nil)
			request.AddCookie(&http.Cookie{Name: loginStateCookieName, Value: "state-a"})
			if test.centralState != "" {
				request.AddCookie(&http.Cookie{Name: "portal_session_oidc_state", Value: test.centralState})
			}
			recorder := httptest.NewRecorder()
			fixture.router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized || fixture.repository.consumeCalls != 0 {
				t.Fatalf("status=%d consumed=%d", recorder.Code, fixture.repository.consumeCalls)
			}
			if cookie := findCookie(t, recorder, loginStateCookieName); cookie == nil || cookie.MaxAge != -1 {
				t.Fatalf("failed central callback retained browser state: %#v", cookie)
			}
		})
	}
}

// centralCallbackRepository 只替换桥接票据持久化；其余仓储端口不在该 HTTP 用例中调用。
type centralCallbackRepository struct {
	*account.GORMRepository
	attachCalls int
	stateHash   string
}

// AttachActivationTicket 用途：记录已验证浏览器状态后的票据写入边界。
// 参数：忽略请求和截止时间；stateHash 为状态摘要；ticket 为合成票据密文。
// 返回值：含原 nonce 和 PKCE 的合成激活上下文；不访问数据库。
func (r *centralCallbackRepository) AttachActivationTicket(_ context.Context, stateHash string, ticket []byte, _ time.Time) (*account.ActivationContext, error) {
	r.attachCalls++
	r.stateHash = stateHash
	return &account.ActivationContext{
		NonceCipher: []byte("nonce-a"), PKCEVerifierCipher: []byte("verifier-a"),
		ReturnPath: "/customer-portal/", CentralTicketCipher: ticket,
	}, nil
}

// TestPortalCentralBridgePreservesValidBrowserState 用途：验证桥接前浏览器绑定及第二阶段状态保留。
// 参数：t 为缺失、失配和合法浏览器 Cookie 的 HTTP 测试上下文。
// 返回值：无；未绑定却持久化票据，或合法桥接清除状态时测试失败。
func TestPortalCentralBridgePreservesValidBrowserState(t *testing.T) {
	for _, browser := range []string{"", "state-b", "state-a"} {
		t.Run(browser, func(t *testing.T) {
			repository := &centralCallbackRepository{}
			oidc := &authEntryOIDC{}
			client, err := central.NewHTTPClient("https://platform.example", "https://platform.example", "portal-client", "https://portal.example/customer-portal/auth/callback")
			if err != nil {
				t.Fatal(err)
			}
			service := account.NewServiceWithOptions(repository, oidc, nil, authEntryProtector{},
				account.SystemClock{}, account.CryptoRandom{}, "catalog", time.Hour, "test",
				account.WithCentralSessions(client, central.Expectation{}))
			config := Config{CentralSessionEnabled: true, PathPrefix: "/customer-portal", SessionCookieName: "portal_session"}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/customer-portal/auth/callback?state=state-a&session_link_ticket=ticket-a", nil)
			ctx.Request.AddCookie(&http.Cookie{Name: "portal_session_oidc_state", Value: "state-a"})
			if browser != "" {
				ctx.Request.AddCookie(&http.Cookie{Name: loginStateCookieName, Value: browser})
			}
			completeLogin(RouterDependencies{Config: config, Account: service})(ctx)
			ctx.Writer.WriteHeaderNow()
			if browser != "state-a" {
				if recorder.Code != http.StatusUnauthorized || repository.attachCalls != 0 {
					t.Fatalf("unbound bridge: status=%d writes=%d", recorder.Code, repository.attachCalls)
				}
				if cookie := findCookie(t, recorder, loginStateCookieName); cookie == nil || cookie.MaxAge != -1 {
					t.Fatalf("unbound bridge retained state: %#v", cookie)
				}
				return
			}
			if recorder.Code != http.StatusFound || repository.attachCalls != 1 || oidc.state != "state-a" {
				t.Fatalf("valid bridge: status=%d writes=%d state=%q", recorder.Code, repository.attachCalls, oidc.state)
			}
			if findCookie(t, recorder, loginStateCookieName) != nil || findCookie(t, recorder, "portal_session_oidc_state") != nil {
				t.Fatal("intermediate bridge must preserve both existing cookies for the OIDC callback")
			}
			if repository.stateHash == "state-a" || recorder.Header().Get("Location") != "https://sso.example/oauth2/authorize?state=state-a" {
				t.Fatal("bridge changed original state or persisted its plaintext")
			}
		})
	}
}
