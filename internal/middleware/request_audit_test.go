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
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/customers", nil))
			if store.completion.UserLoginIP != "" {
				t.Fatalf("login IP=%q completion=%+v", loginIP, store.completion)
			}
		})
	}
}

func TestRequestAuditSkipsInfrastructureProbes(t *testing.T) {
	store := &requestAuditCapture{}
	router := gin.New()
	router.Use(RequestID(), RequestAudit(store, RequestAuditOptions{TenantID: "tenant-a", ApplicationCode: "customer_and_opportunity", EnvironmentCode: "test"}))
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/customer-portal/readyz", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/healthz", "/customer-portal/readyz"} {
		store.start = requestaudit.Start{}
		store.completion = requestaudit.Completion{}
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if store.start.EventID != "" {
			t.Fatalf("probe %s should not create an audit reservation", path)
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
