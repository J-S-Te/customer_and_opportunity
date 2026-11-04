package portalbootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
)

type loginStateRepository struct {
	account.Repository
	err error
}

// CreateActivation 仅模拟本用例所需的登录事务持久化结果。
// 参数：ctx为请求，value为真实服务生成的合成登录事务。
// 返回值：指定存储结果；不模拟身份校验或业务登录成功。
func (r loginStateRepository) CreateActivation(context.Context, *account.ActivationContext) error {
	return r.err
}

type loginStateOIDC struct{ account.OIDCClient }

// AuthorizationURL 提供包含原始state的合成提供方地址以核对Cookie绑定。
// 参数：state为随机状态，nonce为原随机量，codeChallenge为PKCE摘要，returnPath为回跳路径。
// 返回值：仅供HTTP入口断言的地址，不访问真实提供方。
func (loginStateOIDC) AuthorizationURL(state, nonce, codeChallenge, returnPath string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

// TestPortalNormalLoginStateCookie 用途：验证普通及中央登录都建立浏览器 state 绑定。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
func TestPortalNormalLoginStateCookie(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := validPortalConfig()
		config.CentralSessionEnabled = enabled
		response := requestPortalLogin(t, config, nil)
		if response.Code != http.StatusFound {
			t.Fatalf("login start status=%d", response.Code)
		}
		cookies := response.Result().Cookies()
		if !enabled {
			// 普通登录必须把原始 state 绑定到当前浏览器；M-3 的登录 state Cookie 不能省略。
			if len(cookies) != 1 || cookies[0].Name != loginStateCookieName {
				t.Fatalf("legacy login must issue exactly one browser state cookie, got %#v", cookies)
			}
			continue
		}
		if len(cookies) != 2 {
			t.Fatalf("central normal login needs browser and central state cookies, got %d", len(cookies))
		}
		var cookie *http.Cookie
		for _, candidate := range cookies {
			if candidate.Name == config.SessionCookieName+"_oidc_state" {
				cookie = candidate
			}
		}
		if cookie == nil {
			t.Fatalf("central state cookie missing: %#v", cookies)
		}
		target, _ := url.Parse(response.Header().Get("Location"))
		if cookie.Value == "" ||
			cookie.Value != target.Query().Get("state") || cookie.Path != config.PathPrefix ||
			!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
			cookie.MaxAge != 600 || !cookie.Expires.After(time.Now()) ||
			cookie.Expires.After(time.Now().Add(11*time.Minute)) {
			t.Fatal("central login state cookie does not match the protected browser transaction")
		}
	}
}

// TestPortalLoginFailureDoesNotIssueStateCookie 保证存储失败不产生可继续认证的浏览器状态。
// 参数：t为HTTP登录入口的依赖失败测试上下文。
// 返回值：无；失败仍跳转或发Cookie即失败。
func TestPortalLoginFailureDoesNotIssueStateCookie(t *testing.T) {
	config := validPortalConfig()
	config.CentralSessionEnabled = true
	response := requestPortalLogin(t, config, errors.New("isolated storage unavailable"))
	if response.Code != http.StatusInternalServerError || len(response.Result().Cookies()) != 0 {
		t.Fatal("failed login start issued browser state or hid storage failure")
	}
}

// requestPortalLogin 调用真实普通登录处理器，不建立任何业务会话。
// 参数：t为测试上下文，config为合成配置，storageError为指定登录事务写入错误。
// 返回值：原生HTTP记录器，供响应与Cookie断言。
func requestPortalLogin(t *testing.T, config Config, storageError error) *httptest.ResponseRecorder {
	t.Helper()
	protector, err := NewSecretProtector(config.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	service := account.NewService(loginStateRepository{err: storageError}, loginStateOIDC{}, nil,
		protector, account.SystemClock{}, account.CryptoRandom{}, config.RoleConfigHash, time.Hour)
	response := httptest.NewRecorder()
	request, _ := gin.CreateTestContext(response)
	request.Request = httptest.NewRequest(http.MethodGet, "/customer-portal/auth/login", nil)
	beginLogin(RouterDependencies{Config: config, Account: service})(request)
	return response
}
