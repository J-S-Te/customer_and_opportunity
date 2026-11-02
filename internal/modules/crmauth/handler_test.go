package crmauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSessionCookieIsIndependentHttpOnlySecureLaxAndPathScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{options: HTTPOptions{PathPrefix: "/customer-opportunity", CookieName: "customer_opportunity_session", CookieSecure: true}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	handler.setCookie(context, "opaque-session", time.Now().Add(time.Minute))
	value := recorder.Header().Get("Set-Cookie")
	for _, expected := range []string{"customer_opportunity_session=opaque-session", "Path=/customer-opportunity", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(value, expected) {
			t.Fatalf("Set-Cookie %q missing %q", value, expected)
		}
	}
}

func TestLogoutUsesDiscoveredEndSessionEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{options: HTTPOptions{
		PathPrefix:            "/customer-opportunity",
		PublicOrigin:          "https://crm.example.com",
		CookieName:            "customer_opportunity_session",
		EndSessionEndpoint:    "https://identity.example.com/realms/crm/protocol/openid-connect/logout?provider=keycloak",
		ClientID:              "crm-web",
		PostLogoutRedirectURI: "https://crm.example.com/customer-opportunity/",
	}}
	recorder := performLogout(handler)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if location.Scheme != "https" || location.Host != "identity.example.com" || location.Path != "/realms/crm/protocol/openid-connect/logout" {
		t.Fatalf("unexpected logout endpoint %q", location.String())
	}
	query := location.Query()
	if query.Get("provider") != "keycloak" || query.Get("client_id") != "crm-web" || query.Get("post_logout_redirect_uri") != "https://crm.example.com/customer-opportunity/" {
		t.Fatalf("unexpected logout query %v", query)
	}
}

func TestLogoutOmitsCrossOriginPostLogoutRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{options: HTTPOptions{
		PathPrefix:            "/customer-opportunity",
		PublicOrigin:          "https://crm.example.com",
		EndSessionEndpoint:    "https://identity.example.com/logout",
		ClientID:              "crm-web",
		PostLogoutRedirectURI: "https://evil.example/collect",
	}}
	recorder := performLogout(handler)

	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := location.Query().Get("post_logout_redirect_uri"); got != "" {
		t.Fatalf("cross-origin post_logout_redirect_uri = %q, want empty", got)
	}
}

func TestLogoutFallsBackToLocalApplicationWhenEndpointIsInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{options: HTTPOptions{
		PathPrefix:         "/customer-opportunity",
		EndSessionEndpoint: "javascript:alert(1)",
	}}
	recorder := performLogout(handler)

	if location := recorder.Header().Get("Location"); location != "/customer-opportunity/" {
		t.Fatalf("Location = %q, want local application", location)
	}
}

// 活动接口只接受持有效会话 Cookie 的浏览器请求；成功信封返回权威空闲窗口，
// 且只有该接口推进 last_interactive_at。CSRF/Origin 防护由挂载它的写路由组统一执行。
func TestActivityEndpointRequiresValidSessionAndRecordsInteraction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	repo := newMemoryRepo()
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(24 * time.Hour)
	claims.RefreshToken = "refresh-token"
	service := newTestService(t, repo, &fakeOIDC{exchanged: claims, current: claims}, now)
	handler := NewHandler(service, HTTPOptions{CookieName: "crm_session"})
	router := gin.New()
	router.POST("/auth/activity", handler.Activity)

	// 无会话 Cookie：仍返回既有的 401 未认证信封。
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/auth/activity", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing cookie status = %d", recorder.Code)
	}

	token := loginSession(t, service)
	service.now = func() time.Time { return now.Add(26 * time.Minute) }
	request := httptest.NewRequest(http.MethodPost, "/auth/activity", nil)
	request.AddCookie(&http.Cookie{Name: "crm_session", Value: token})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("activity status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Code string `json:"code"`
		Data struct {
			IdleTimeoutSeconds int   `json:"idle_timeout_seconds"`
			IdleRemainingMS    int64 `json:"idle_remaining_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}
	if envelope.Code != "OK" || envelope.Data.IdleTimeoutSeconds != 1800 || envelope.Data.IdleRemainingMS != (30*time.Minute).Milliseconds() {
		t.Fatalf("activity response = %+v", envelope)
	}
	if interactive := repo.sessions[tokenHash(token)].LastInteractiveAt; interactive == nil || !interactive.Equal(now.Add(26*time.Minute)) {
		t.Fatalf("last_interactive_at = %v", interactive)
	}

	// 空闲期限之后同一 Cookie 不能再续期。
	service.now = func() time.Time { return now.Add(60 * time.Minute) }
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("idle-expired activity status = %d", recorder.Code)
	}
}

func performLogout(handler *Handler) *httptest.ResponseRecorder {
	router := gin.New()
	router.POST("/customer-opportunity/auth/logout", handler.Logout)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/customer-opportunity/auth/logout", nil))
	return recorder
}

func TestRequireSameOriginRejectsCrossOriginAndMissingCSRFHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{options: HTTPOptions{PublicOrigin: "https://crm.example.com"}}
	tests := []struct {
		name, origin, csrf string
		allowed            bool
	}{
		{"valid", "https://crm.example.com", "1", true},
		{"cross origin", "https://evil.example", "1", false},
		{"missing header", "https://crm.example.com", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-CSRF-Token", test.csrf)
			context.Request = request
			handler.RequireSameOrigin(context)
			if test.allowed && context.IsAborted() {
				t.Fatal("valid same-origin request was rejected")
			}
			if !test.allowed && (!context.IsAborted() || recorder.Code != http.StatusForbidden) {
				t.Fatalf("status=%d aborted=%v", recorder.Code, context.IsAborted())
			}
		})
	}
}
