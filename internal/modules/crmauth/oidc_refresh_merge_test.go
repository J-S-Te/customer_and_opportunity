package crmauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

// TestRefreshFailuresPreserveCentralDependencyClassification 验证中央依赖错误不会沿用旧模式误注销。
// 参数：t为真实HTTP刷新错误分类测试上下文，材料均为合成标记。
// 返回值：无；明确令牌失效或暂时依赖错误分类漂移时失败。
func TestRefreshFailuresPreserveCentralDependencyClassification(t *testing.T) {
	for _, item := range []struct {
		name, code string
		central    bool
		rejected   bool
	}{
		{"legacy-client-rejected", "invalid_client", false, true},
		{"central-client-unavailable", "invalid_client", true, false},
		{"central-unauthorized-client-unavailable", "unauthorized_client", true, false},
		{"central-grant-rejected", "invalid_grant", true, true},
		{"central-token-rejected", "invalid_token", true, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"` + item.code + `"}`))
			}))
			defer server.Close()
			client := &platformOIDCClient{httpClient: server.Client(), config: oauth2.Config{
				ClientID: "qa-client", Endpoint: oauth2.Endpoint{
					TokenURL: server.URL, AuthStyle: oauth2.AuthStyleInParams,
				},
			}}
			if item.central {
				client.central = &centralTestClient{}
			}
			_, err := client.RefreshToken(context.Background(), "qa-refresh")
			if err == nil || errors.Is(err, errRefreshRejected) != item.rejected {
				t.Fatalf("rejected=%v, want %v: %v", errors.Is(err, errRefreshRejected), item.rejected, err)
			}
		})
	}
}
