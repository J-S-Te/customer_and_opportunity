package crmauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSessionHTTPFailuresPreserveDependencyStatus 验证桥接和状态入口不把数据库故障误报为退出。
// 参数：t为HTTP错误边界测试上下文。
// 返回值：无；明确失效非401或暂时故障非503时失败。
func TestSessionHTTPFailuresPreserveDependencyStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, item := range []struct {
		err    error
		status int
	}{
		{ErrUnauthenticated, http.StatusUnauthorized},
		{errors.New("database unavailable"), http.StatusServiceUnavailable},
		{errors.Join(ErrUnauthenticated, ErrAuthorizationUnavailable), http.StatusServiceUnavailable},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/auth/session", nil)
		writeSessionFailure(c, item.err)
		if w.Code != item.status {
			t.Fatalf("session failure status = %d, want %d", w.Code, item.status)
		}
	}
}
