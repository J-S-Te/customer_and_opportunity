package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

// local-logout 无认证会话校验，必须挂仅 Origin 精确校验的同源防护，防止跨站盲 POST 强制注销。
func TestLocalLogoutRouteRequiresSameOriginGuard(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, `"/auth/local-logout"`) {
			continue
		}
		if !strings.Contains(line, `middleware.RequireOriginMatch(config.PublicOrigin)`) || !strings.Contains(line, "authHandler.LocalLogout") {
			t.Fatalf("local-logout route missing origin guard: %s", line)
		}
		return
	}
	t.Fatal("local-logout route missing")
}

func TestRequireOriginMatchBlocksCrossSiteLocalLogout(t *testing.T) {
	t.Parallel()
	router := gin.New()
	router.POST("/auth/local-logout", middleware.RequireOriginMatch("https://crm.example.com"), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	tests := []struct {
		name, origin string
		want         int
	}{
		{name: "same origin passes", origin: "https://crm.example.com", want: http.StatusNoContent},
		{name: "cross site blocked", origin: "https://evil.example", want: http.StatusForbidden},
		{name: "missing origin fail closed", origin: "", want: http.StatusForbidden},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, "/auth/local-logout", nil)
		if test.origin != "" {
			request.Header.Set("Origin", test.origin)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("%s: status=%d want=%d", test.name, recorder.Code, test.want)
		}
	}
}
