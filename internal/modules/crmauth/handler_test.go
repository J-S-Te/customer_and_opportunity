package crmauth

import (
	"net/http"
	"net/http/httptest"
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
