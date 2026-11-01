package account

import (
	"context"
	"errors"
	"testing"
	"time"

	central "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/sessiongovernance"
)

type portalCentralClient struct {
	central.Client
	err        error
	activities int
}

type unavailableSessionRepository struct{ Repository }

// FindSession 模拟真实存储故障，不能将其解释为Cookie失效。
// 参数：请求、租户、Cookie摘要及当前时间。
// 返回值：空会话和存储错误。
func (unavailableSessionRepository) FindSession(context.Context, string, string, time.Time) (*Session, error) {
	return nil, errors.New("isolated database temporarily unavailable")
}

// TestPortalSessionLookupOutageIsUnavailable 验证门户数据库故障不会触发强制重登。
// 参数：t为服务层错误边界测试上下文。
// 返回值：无；确定退出与暂时故障混淆时失败。
func TestPortalSessionLookupOutageIsUnavailable(t *testing.T) {
	s := &Service{repo: unavailableSessionRepository{}, centralClient: &portalCentralClient{}, clock: fixedClock{now: time.Now()}}
	if _, err := s.AuthenticateSession(context.Background(), "tenant", "opaque", false); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("database outage must remain unavailable, got %v", err)
	}
}

// State 提供单独客户门户的中央验证结果，不触碰客户角色或绑定。
// 参数：请求及服务器绑定秘密。
// 返回值：空窗口和指定错误。
func (c *portalCentralClient) State(context.Context, string) (central.Context, error) {
	return central.Context{}, c.err
}

// Activity 统计显式活动；普通鉴权不应调用本方法。
// 参数：请求及服务器绑定秘密。
// 返回值：空窗口和指定错误。
func (c *portalCentralClient) Activity(context.Context, string) (central.Context, error) {
	c.activities++
	return central.Context{}, c.err
}

// TestPortalCentralFailurePreservesCustomerBoundary 验证旧记录不续起点，故障不变成确定登出。
// 参数：t为测试上下文。
// 返回值：无；错误分类、客户范围或活动来源不符即失败。
func TestPortalCentralFailurePreservesCustomerBoundary(t *testing.T) {
	for _, test := range []struct {
		binding        []byte
		upstream, want error
	}{
		{nil, nil, ErrInvalidLoginState}, {[]byte("test-binding"), central.ErrInactive, ErrInvalidLoginState},
		{[]byte("test-binding"), central.ErrUnavailable, ErrAuthorizationUnavailable},
	} {
		client := &portalCentralClient{err: test.upstream}
		service := &Service{centralClient: client, protector: passthroughProtector{}}
		session := &Session{CustomerID: 42, CentralBindingCipher: test.binding}
		_, err := service.readCentral(context.Background(), session, false)
		if !errors.Is(err, test.want) || session.CustomerID != 42 || client.activities != 0 {
			t.Fatalf("central failure changed customer or session semantics: %v", err)
		}
	}
}

// TestPortalDisabledCentralLifecycleIsUnavailable 验证分阶段发布不会把未启用统一管理误判为退出。
// 参数：t为门户独立会话兼容边界测试上下文。
// 返回值：无；平台总开关未启用时返回确定失效即失败。
func TestPortalDisabledCentralLifecycleIsUnavailable(t *testing.T) {
	service := &Service{}
	if _, err := service.Lifecycle(context.Background(), "tenant", "existing-cookie", false); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("disabled central management must remain unavailable, got %v", err)
	}
}
