package crmauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
)

type serialAuthorizationRepository struct {
	*memoryRepo
	rowMu, stateMu sync.Mutex
	attempts       chan struct{}
}
type serialAuthorizationOIDC struct {
	fakeOIDC
	first, next      verifiedClaims
	entered, release chan struct{}
	calls            int
}

// FindSession 模拟普通快照读，不阻塞另一个事务持有的会话行锁。
// 参数：ctx为请求，hash、now和idle为服务端会话边界。
// 返回值：独立快照或失效错误。
func (r *serialAuthorizationRepository) FindSession(ctx context.Context, hash string, now time.Time, idle time.Duration) (*Session, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.memoryRepo.FindSession(ctx, hash, now, idle)
}

// TouchSession 只推进普通观测字段，不能回填旧授权。
// 参数：ctx为请求，其余为会话标识与时间边界。
// 返回值：真实内存存储操作的错误。
func (r *serialAuthorizationRepository) TouchSession(ctx context.Context, hash string, now, checked time.Time, idle time.Duration) error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.memoryRepo.TouchSession(ctx, hash, now, checked, idle)
}

// WithAuthorizationLock 模拟数据库串行行锁与原子提交，使真实认证器面对并发请求。
// 参数：ctx为请求，hash、now和idle限定会话，update为生产在线复核逻辑。
// 返回值：已提交快照或准确错误，未知故障不提交。
func (r *serialAuthorizationRepository) WithAuthorizationLock(ctx context.Context, hash string, now time.Time,
	idle time.Duration, update func(context.Context, *Session) error) (*Session, error) {
	r.attempts <- struct{}{}
	r.rowMu.Lock()
	defer r.rowMu.Unlock()
	current, err := r.FindSession(ctx, hash, now, idle)
	if err != nil {
		return nil, err
	}
	err = update(ctx, current)
	if err != nil && !errors.Is(err, sharedauthorization.ErrForbidden) {
		return nil, err
	}
	r.stateMu.Lock()
	r.sessions[hash] = current
	returned := *current
	r.stateMu.Unlock()
	return &returned, err
}

// UserInfo 在第一份较宽授权读取中制造受控等待，随后返回同版本已缩小的授权。
// 参数：ctx和访问令牌由真实认证器提供。
// 返回值：预设的完整有效授权快照。
func (o *serialAuthorizationOIDC) UserInfo(context.Context, string) (verifiedClaims, error) {
	o.calls++
	if o.calls == 1 {
		close(o.entered)
		<-o.release
		return o.first, nil
	}
	return o.next, nil
}

// TestConcurrentFreshCRMReadsNeverRestoreOlderWideScope 验证并发强制复核在锁内顺序读取当前授权。
// 参数：t为双请求、同Cookie、同revision自然范围变化的内存认证测试上下文。
// 返回值：无；第二次读取被缓存跳过或较宽权限最终回填时失败。
func TestConcurrentFreshCRMReadsNeverRestoreOlderWideScope(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	repo := &serialAuthorizationRepository{memoryRepo: newMemoryRepo(), attempts: make(chan struct{}, 2)}
	first := validClaims(now)
	first.AuthzRevision = 9
	next := first
	next.Permissions = []string{"customer.read"}
	next.DataScopes = []sharedauth.DataScope{{RoleCode: "sales", ScopeType: "SELF", ScopeID: "user-1", EnvironmentCode: "dev"}}
	oidc := &serialAuthorizationOIDC{fakeOIDC: fakeOIDC{exchanged: validClaims(now)}, first: first, next: next, entered: make(chan struct{}), release: make(chan struct{})}
	service := newTestService(t, repo, oidc, now)
	start, _ := service.BeginLogin(context.Background(), "/")
	login, err := service.CompleteLogin(context.Background(), strings.Split(start.AuthorizationURL, "state=")[1], "code")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	ctx := authorizationrefresh.WithForce(context.Background())
	go func() { _, err := service.Authenticate(ctx, login.SessionToken); results <- err }()
	select {
	case <-oidc.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first authorization read did not start")
	}
	go func() { _, err := service.Authenticate(ctx, login.SessionToken); results <- err }()
	for range 2 {
		select {
		case <-repo.attempts:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent request did not enter authorization lock")
		}
	}
	close(oidc.release)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("authorization reads did not finish")
		}
	}
	stored, err := repo.FindSession(context.Background(), tokenHash(login.SessionToken), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := claimsFromSession(stored)
	if err != nil || oidc.calls != 2 || len(claims.Permissions) != 1 || len(claims.DataScopes) != 1 || claims.DataScopes[0].ScopeType != "SELF" {
		t.Fatalf("late response widened persisted authorization: %#v calls=%d err=%v", claims, oidc.calls, err)
	}
}
