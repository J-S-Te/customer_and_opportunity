package portalbootstrap

import (
	"context"
	"errors"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
	"net/http"
	"testing"
)

type dependencyCentralMarker struct{ central.Client }

// TestPortalAuthorizationTransportFailureIsUnavailable 验证门户原请求取消不会触发确定退出。
// 参数：t为不发出实际网络请求的适配器测试上下文。
// 返回值：无；取消丢失依赖语义即失败。
func TestPortalAuthorizationTransportFailureIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter := &OIDCAdapter{central: dependencyCentralMarker{}, platformBaseURL: "http://127.0.0.1:1", httpClient: &http.Client{}}
	if _, err := adapter.authorizationContext(ctx, "access-token"); !errors.Is(err, sharedauthorization.ErrUnavailable) {
		t.Fatalf("cancelled request misclassified: %v", err)
	}
}
