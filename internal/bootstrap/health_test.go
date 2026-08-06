package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthHandlersSeparateLivenessAndReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/livez", livenessHandler)
	router.GET("/readyz", readinessHandler(func(context.Context) bool { return false }))

	live := httptest.NewRecorder()
	router.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK || live.Body.String() != `{"status":"alive"}` {
		t.Fatalf("liveness status=%d body=%s", live.Code, live.Body.String())
	}
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || ready.Body.String() != `{"status":"not_ready"}` {
		t.Fatalf("readiness status=%d body=%s", ready.Code, ready.Body.String())
	}
}
