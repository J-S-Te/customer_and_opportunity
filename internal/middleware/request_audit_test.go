package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/loginip"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
)

type requestAuditCapture struct {
	start       requestaudit.Start
	completion  requestaudit.Completion
	startErr    error
	completeErr error
}

func (c *requestAuditCapture) Start(_ context.Context, value requestaudit.Start) error {
	c.start = value
	return c.startErr
}

func (c *requestAuditCapture) Complete(_ context.Context, _ string, value requestaudit.Completion) error {
	c.completion = value
	return c.completeErr
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

// requestAuditBusinessRouter 注册一条写路由：挂载平台主体并写出业务响应，供强制审计模式用例复用。
func requestAuditBusinessRouter(store requestAuditStore, body string, writes int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test"}))
	router.POST("/customers", func(c *gin.Context) {
		principal := auth.Principal{UserID: "user-a", DisplayName: "User A", TenantID: "tenant-a", LoginIP: "203.0.113.9"}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Header("X-Business-Header", "value")
		c.Status(http.StatusCreated)
		for i := 0; i < writes; i++ {
			_, _ = c.Writer.WriteString(body)
		}
	})
	return router
}

// SEC-D4b 对齐平台：强制审计模式下 Complete 失败必须拒绝请求——扣留的业务响应被丢弃，
// 客户端收到 503 COMMON_AUDIT_UNAVAILABLE，不能只剩分发器补出的匿名“请求已开始→中断”记录却放行成功。
func TestRequestAuditRequiredModeRejectsWhenFinalizeFails(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	store := &requestAuditCapture{completeErr: errors.New("audit outbox unavailable")}
	router := requestAuditBusinessRouter(store, "customer created", 1)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/customers", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "COMMON_AUDIT_UNAVAILABLE") || strings.Contains(response.Body.String(), "customer created") {
		t.Fatalf("body=%s", response.Body.String())
	}
	if response.Header().Get("X-Business-Header") != "" {
		t.Fatal("business headers must not leak when the request is rejected")
	}
	if store.completion.ActorID != "user-a" || store.completion.Route != "/customers" {
		t.Fatalf("completion must still be attempted with the full operation summary: %+v", store.completion)
	}
}

// 强制审计模式 + Complete 成功：扣留的响应原样提交（状态码、头、体）。
func TestRequestAuditRequiredModeFlushesResponseWhenFinalizeSucceeds(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	store := &requestAuditCapture{}
	router := requestAuditBusinessRouter(store, "customer created", 1)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/customers", nil))
	if response.Code != http.StatusCreated || response.Body.String() != "customer created" || response.Header().Get("X-Business-Header") != "value" {
		t.Fatalf("status=%d body=%q header=%q", response.Code, response.Body.String(), response.Header().Get("X-Business-Header"))
	}
	if store.completion.Result != "SUCCESS" || store.completion.HTTPStatus != http.StatusCreated {
		t.Fatalf("completion=%+v", store.completion)
	}
}

// 非强制模式保持旧行为：Complete 失败只记日志，业务响应照常返回（滞留 STARTED 由分发器转中断失败）。
func TestRequestAuditNonRequiredModeKeepsLegacyBehaviorWhenFinalizeFails(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "false")
	store := &requestAuditCapture{completeErr: errors.New("audit outbox unavailable")}
	router := requestAuditBusinessRouter(store, "customer created", 1)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/customers", nil))
	if response.Code != http.StatusCreated || response.Body.String() != "customer created" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

// 大响应溢写临时文件后仍受同一拒绝语义约束，成功时逐字节原样回放，临时文件随即清理。
func TestRequestAuditRequiredModeSpillsLargeResponseAndReplaysIntact(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	store := &requestAuditCapture{}
	chunk := strings.Repeat("x", 1024)
	writes := maxRequestAuditBufferedResponseBytes/len(chunk) + 2
	router := requestAuditBusinessRouter(store, chunk, writes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/customers", nil))
	if response.Code != http.StatusCreated || response.Body.String() != strings.Repeat(chunk, writes) {
		t.Fatalf("status=%d len=%d want=%d", response.Code, response.Body.Len(), len(chunk)*writes)
	}
	assertNoRequestAuditSpillFiles(t, tmp)

	failing := &requestAuditCapture{completeErr: errors.New("audit outbox unavailable")}
	response = httptest.NewRecorder()
	requestAuditBusinessRouter(failing, chunk, writes).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/customers", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), chunk[:64]) {
		t.Fatalf("spilled response must be discarded on finalize failure: status=%d", response.Code)
	}
	assertNoRequestAuditSpillFiles(t, tmp)
}

// 超过硬上限：审计如实记为 FAILURE / AUDIT_RESPONSE_TOO_LARGE，客户端收到 503，不能“审计记成功、客户端拿 503”。
func TestRequestAuditRequiredModeRejectsResponseBeyondSpillHardLimit(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	original := maxRequestAuditSpilledResponseBytes
	maxRequestAuditSpilledResponseBytes = 2048
	defer func() { maxRequestAuditSpilledResponseBytes = original }()
	store := &requestAuditCapture{}
	router := requestAuditBusinessRouter(store, strings.Repeat("x", 1024), 3)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/customers", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "COMMON_AUDIT_RESPONSE_TOO_LARGE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.completion.Result != "FAILURE" || store.completion.ReasonCode != "AUDIT_RESPONSE_TOO_LARGE" || store.completion.RiskLevel != "HIGH" {
		t.Fatalf("completion=%+v", store.completion)
	}
	if store.completion.HTTPStatus != http.StatusCreated {
		t.Fatalf("handler status must be preserved for traceability: %+v", store.completion)
	}
	assertNoRequestAuditSpillFiles(t, tmp)
}

func assertNoRequestAuditSpillFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read TMPDIR %q: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("spill temp files left behind in %s: %d entries", dir, len(entries))
	}
}
