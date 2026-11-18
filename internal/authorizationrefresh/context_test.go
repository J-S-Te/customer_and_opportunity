package authorizationrefresh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFromRequestRestrictsFreshReads 验证浏览器标记只在准确自描述读生效。
// 参数：t为方法、路径与重复头边界的测试上下文。
// 返回值：无；业务路径、非GET、错误值或相近路径启用强制复核时失败。
func TestFromRequestRestrictsFreshReads(t *testing.T) {
	for _, item := range []struct {
		method, target, value string
		duplicate, want       bool
	}{
		{"GET", "/api/v1/auth/me", "1", false, true},
		{"GET", "/customer-portal/api/v1/auth/me", "1", false, true},
		{"GET", "/auth/session", "1", false, true},
		{"GET", "/oauth2/authorization-context", "1", false, true},
		{"GET", "/api/v1/customers", "1", false, false},
		{"GET", "/api/v1/auth/me/", "1", false, false},
		{"GET", "/api/v1/auth/metadata", "1", false, false},
		{"POST", "/api/v1/auth/me", "1", false, false},
		{"HEAD", "/api/v1/auth/me", "1", false, false},
		{"GET", "/api/v1/auth/me", "true", false, false},
		{"GET", "/api/v1/auth/me", "1", true, false},
	} {
		request := httptest.NewRequest(item.method, item.target, nil)
		request.Header.Set(Header, item.value)
		if item.duplicate {
			request.Header.Add(Header, "1")
		}
		if got := Forced(FromRequest(context.Background(), request)); got != item.want {
			t.Fatalf("%s %s value=%q duplicate=%t forced=%t", item.method, item.target, item.value, item.duplicate, got)
		}
	}
	if Forced(FromRequest(context.Background(), nil)) {
		t.Fatal("nil request enabled refresh")
	}
	request := &http.Request{Method: http.MethodGet}
	if Forced(FromRequest(context.Background(), request)) {
		t.Fatal("missing URL enabled refresh")
	}
}
