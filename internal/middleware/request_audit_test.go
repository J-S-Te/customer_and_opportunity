package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
)

type requestAuditCapture struct {
	start      requestaudit.Start
	completion requestaudit.Completion
	startErr   error
}

func (c *requestAuditCapture) Start(_ context.Context, value requestaudit.Start) error {
	c.start = value
	return c.startErr
}

func (c *requestAuditCapture) Complete(_ context.Context, _ string, value requestaudit.Completion) error {
	c.completion = value
	return nil
}

func TestRequestAuditCapturesPlatformPrincipalAndNormalizedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &requestAuditCapture{}
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test"}))
	router.POST("/customers/:id", func(c *gin.Context) {
		principal := auth.Principal{UserID: "user-a", DisplayName: "User A", TenantID: "tenant-a", LoginIP: "203.0.113.9"}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Status(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/customers/42?secret=must-not-persist", nil)
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
	if store.start.Method != http.MethodPost || store.start.RequestID == "" || store.start.OccurredAt.IsZero() {
		t.Fatalf("start=%+v", store.start)
	}
	if store.completion.ActorType != "USER" || store.completion.ActorID != "user-a" || store.completion.Route != "/customers/:id" {
		t.Fatalf("completion=%+v", store.completion)
	}
	if store.completion.UserLoginIP != "203.0.113.9" {
		t.Fatalf("user login IP=%q", store.completion.UserLoginIP)
	}
	if store.completion.Action != "HTTP_POST /customers/:id" || store.completion.Result != "SUCCESS" || store.completion.RiskLevel != "HIGH" {
		t.Fatalf("completion=%+v", store.completion)
	}
}

// AUD-IP-2026：办公网/VPN/回环都是合法登录地址，只有空值与非法字面量才丢弃。
func TestRequestAuditKeepsValidLoginIPAndOmitsInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct{ loginIP, want string }{
		{"", ""},
		{"172.18.0.2", "172.18.0.2"},
		{"10.8.0.5", "10.8.0.5"},
		{"not-an-ip", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.loginIP, func(t *testing.T) {
			store := &requestAuditCapture{}
			router := gin.New()
			router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test"}))
			router.GET("/customers", func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{UserID: "user-a", LoginIP: testCase.loginIP}))
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/customers", nil)
			req.RemoteAddr = "172.18.0.12:8080"
			router.ServeHTTP(httptest.NewRecorder(), req)
			if store.completion.UserLoginIP != testCase.want {
				t.Fatalf("login IP=%q want %q completion=%+v", store.completion.UserLoginIP, testCase.want, store.completion)
			}
		})
	}
}

func TestRequestAuditFallsBackToForwardedPublicIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &requestAuditCapture{}
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "prod"}))
	router.GET("/customers", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{UserID: "user-a", DisplayName: "User A", LoginIP: "172.18.0.17"}))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/customers", nil)
	req.RemoteAddr = "172.18.0.12:8080"
	req.Header.Set("X-Forwarded-For", "198.51.100.99, 125.120.19.87")
	router.ServeHTTP(httptest.NewRecorder(), req)

	// 登录地址存在（即使是办公网地址）就不再回退，操作地址独立记录。
	if store.completion.UserLoginIP != "172.18.0.17" {
		t.Fatalf("user login IP=%q, want preserved login IP", store.completion.UserLoginIP)
	}
	if store.completion.ClientIP != "172.18.0.12" || store.completion.IPSource != "DIRECT" {
		t.Fatalf("client IP=%q source=%q，非可信对端不得采信转发头", store.completion.ClientIP, store.completion.IPSource)
	}
}

// 登录地址缺失时才回退到可信代理解析出的地址，并标记来源。
func TestRequestAuditFallsBackToTrustedProxyAddressOnlyWhenLoginIPMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &requestAuditCapture{}
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{
		TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "prod",
		TrustedProxies: mustTrusted("172.18.0.12/32"),
	}))
	router.GET("/customers", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{UserID: "user-a"}))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/customers", nil)
	req.RemoteAddr = "172.18.0.12:8080"
	req.Header.Set("X-Forwarded-For", "198.51.100.99, 125.120.19.87")
	router.ServeHTTP(httptest.NewRecorder(), req)

	if store.completion.ClientIP != "125.120.19.87" || store.completion.IPSource != "TRUSTED_PROXY" {
		t.Fatalf("client IP=%q source=%q，可信代理后应取最右侧非代理地址", store.completion.ClientIP, store.completion.IPSource)
	}
}

func mustTrusted(value string) []netip.Prefix {
	prefixes, err := loginip.ParseTrusted(value)
	if err != nil {
		panic(err)
	}
	return prefixes
}

func TestRequestClientIPRejectsPrivateOnlyAddresses(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/customers", nil)
	req.RemoteAddr = "172.18.0.12:8080"
	req.Header.Set("X-Forwarded-For", "172.18.0.17")
	if got := loginip.FromRequest(req); got != "" {
		t.Fatalf("request client IP=%q, want empty for private addresses", got)
	}
}

func TestRequestClientIPIgnoresSpoofedHeadersFromPublicPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/customers", nil)
	req.RemoteAddr = "203.0.113.5:12345"
	req.Header.Set("X-Real-IP", "8.8.8.8")
	req.Header.Set("X-Forwarded-For", "8.8.8.8, 1.2.3.4")
	if got := loginip.FromRequest(req); got != "203.0.113.5" {
		t.Fatalf("request client IP=%q, want direct public peer 203.0.113.5", got)
	}
}

func TestRequestAuditSkipsInfrastructureProbes(t *testing.T) {
	store := &requestAuditCapture{}
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test"}))
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/livez", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/readyz/", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/customer-portal/readyz", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/customer-portal/livez/", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/healthz", "/livez", "/readyz/", "/customer-portal/readyz", "/customer-portal/livez/"} {
		store.start = requestaudit.Start{}
		store.completion = requestaudit.Completion{}
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if store.start.EventID != "" {
			t.Fatalf("probe %s should not create an audit reservation", path)
		}
	}
}

func TestIsProbePathDoesNotSkipBusinessRoutes(t *testing.T) {
	for _, path := range []string{"/api/healthz/report", "/healthz-export", "/business/livez/history"} {
		if isProbePath(path) {
			t.Fatalf("business path %q classified as probe", path)
		}
	}
}

func TestIsProbePathRecognizesExactNestedAndTrailingSlashProbes(t *testing.T) {
	for _, path := range []string{
		"/healthz", "/readyz", "/livez",
		"/healthz/", "/readyz/", "/livez/",
		"/customer-portal/healthz", "/customer-portal/readyz", "/customer-portal/livez",
	} {
		if !isProbePath(path) {
			t.Fatalf("probe path %q was not recognized", path)
		}
	}
}

func TestRequestAuditFailsClosedBeforeHandlerWhenReservationFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &requestAuditCapture{startErr: errors.New("database unavailable")}
	called := false
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{}))
	router.GET("/customers", func(c *gin.Context) { called = true })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/customers", nil))
	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status=%d called=%v", response.Code, called)
	}
	if !store.completion.CompletedAt.Equal(time.Time{}) {
		t.Fatalf("unexpected completion=%+v", store.completion)
	}
}
