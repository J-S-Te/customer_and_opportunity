package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestRequestAuditOmitsInvalidOrUnavailableUserLoginIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, loginIP := range []string{"", "172.18.0.2", "not-an-ip"} {
		t.Run(loginIP, func(t *testing.T) {
			store := &requestAuditCapture{}
			router := gin.New()
			router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test"}))
			router.GET("/customers", func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{UserID: "user-a", LoginIP: loginIP}))
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/customers", nil)
			req.RemoteAddr = "172.18.0.12:8080"
			router.ServeHTTP(httptest.NewRecorder(), req)
			if store.completion.UserLoginIP != "" {
				t.Fatalf("login IP=%q completion=%+v", loginIP, store.completion)
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

	if store.completion.UserLoginIP != "125.120.19.87" {
		t.Fatalf("fallback user login IP=%q, want forwarded public IP", store.completion.UserLoginIP)
	}
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
