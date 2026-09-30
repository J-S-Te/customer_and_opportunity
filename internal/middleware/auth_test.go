package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireSameOriginWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, method, origin, csrf string
		status                     int
	}{
		{"safe read", http.MethodGet, "", "", http.StatusNoContent},
		{"same-origin write", http.MethodPost, "https://crm.example.com", "1", http.StatusNoContent},
		{"cross-origin", http.MethodPost, "https://evil.example", "1", http.StatusForbidden},
		{"missing custom header", http.MethodPut, "https://crm.example.com", "", http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequireSameOriginWrite("https://crm.example.com"))
			router.Handle(test.method, "/api/v1/resource", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(test.method, "/api/v1/resource", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.csrf != "" {
				request.Header.Set("X-CSRF-Token", test.csrf)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

// local-logout 是不带自定义 CSRF 头的裸 fetch POST，只能依赖浏览器自动携带的 Origin 做同源防护；
// 缺失 Origin 也必须拒绝（fail-closed），否则跨站盲 POST 仍可强制注销。
func TestRequireOriginMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, method, origin string
		status               int
	}{
		{"same-origin post", http.MethodPost, "https://crm.example.com", http.StatusNoContent},
		{"cross-origin post", http.MethodPost, "https://evil.example", http.StatusForbidden},
		{"missing origin", http.MethodPost, "", http.StatusForbidden},
		{"origin scheme mismatch", http.MethodPost, "http://crm.example.com", http.StatusForbidden},
		{"origin port mismatch", http.MethodPost, "https://crm.example.com:8443", http.StatusForbidden},
		{"safe method without origin", http.MethodGet, "", http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequireOriginMatch("https://crm.example.com"))
			router.Handle(test.method, "/auth/local-logout", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(test.method, "/auth/local-logout", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}
