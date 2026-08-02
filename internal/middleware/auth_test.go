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
