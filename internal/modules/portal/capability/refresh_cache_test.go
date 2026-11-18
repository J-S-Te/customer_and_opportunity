package capability

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/authorizationrefresh"
)

type refreshReaderFunc func(context.Context, string, uint64) (Options, error)

// Get 以受控来源替换外部能力数据库，保留真实缓存逻辑。
// 参数：ctx为请求，tenant和customer为可信客户边界。
// 返回值：当前来源的能力或故障。
func (f refreshReaderFunc) Get(ctx context.Context, tenant string, customer uint64) (Options, error) {
	return f(ctx, tenant, customer)
}

// TestFreshCapabilityReadBypassesOnlyRequestedCustomerCache 验证强制读更新能力交集但保留普通TTL与其他客户缓存。
// 参数：t为真实CachedReader与受控能力来源的测试上下文。
// 返回值：无；新鲜要求被忽略、业务请求缓存失效或客户缓存串扰时失败。
func TestFreshCapabilityReadBypassesOnlyRequestedCustomerCache(t *testing.T) {
	calls := 0
	current := DefaultOptions()
	source := refreshReaderFunc(func(context.Context, string, uint64) (Options, error) { calls++; return current, nil })
	reader := NewCachedReader(source, 30*time.Second)
	now := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	reader.now = func() time.Time { return now }
	for _, customer := range []uint64{7, 8} {
		if _, err := reader.Get(context.Background(), "tenant", customer); err != nil {
			t.Fatal(err)
		}
	}
	current.ReportEnabled = false
	if options, err := reader.Get(context.Background(), "tenant", 7); err != nil || !options.ReportEnabled || calls != 2 {
		t.Fatal("ordinary request lost the existing TTL")
	}
	options, err := reader.Get(authorizationrefresh.WithForce(context.Background()), "tenant", 7)
	if err != nil || options.ReportEnabled || calls != 3 {
		t.Fatalf("fresh capability=%#v calls=%d error=%v", options, calls, err)
	}
	if options, _ := reader.Get(context.Background(), "tenant", 7); options.ReportEnabled || calls != 3 {
		t.Fatal("fresh result was not cached for ordinary requests")
	}
	if options, _ := reader.Get(context.Background(), "tenant", 8); !options.ReportEnabled || calls != 3 {
		t.Fatal("another customer's cache was changed")
	}
	now = now.Add(31 * time.Second)
	if options, _ := reader.Get(context.Background(), "tenant", 8); options.ReportEnabled || calls != 4 {
		t.Fatal("normal cache did not expire")
	}
}

// TestFreshCapabilityFailureCannotRestoreOldEntry 验证刷新失败不回退或继续缓存旧宽能力。
// 参数：t为已缓存允许能力及来源故障的测试上下文。
// 返回值：无；故障成功兜底或恢复后仍复用旧能力时失败。
func TestFreshCapabilityFailureCannotRestoreOldEntry(t *testing.T) {
	current, failure := DefaultOptions(), error(nil)
	reader := NewCachedReader(refreshReaderFunc(func(context.Context, string, uint64) (Options, error) { return current, failure }), 30*time.Second)
	if _, err := reader.Get(context.Background(), "tenant", 7); err != nil {
		t.Fatal(err)
	}
	failure = context.DeadlineExceeded
	if _, err := reader.Get(authorizationrefresh.WithForce(context.Background()), "tenant", 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fresh outage=%v", err)
	}
	current.ReportEnabled, failure = false, nil
	if options, err := reader.Get(context.Background(), "tenant", 7); err != nil || options.ReportEnabled {
		t.Fatalf("old capability entry survived: %#v %v", options, err)
	}
}

// TestOlderCapabilityReadCannotRefillAfterForce 验证旧在途读取不能覆盖强制读取的新能力。
// 参数：t为并发普通读和强制读的缓存测试上下文。
// 返回值：无；较宽旧能力迟到后成为缓存结果时失败。
func TestOlderCapabilityReadCannotRefillAfterForce(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	reader := NewCachedReader(refreshReaderFunc(func(context.Context, string, uint64) (Options, error) {
		options := DefaultOptions()
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return options, nil
		}
		options.ReportEnabled = false
		return options, nil
	}), 30*time.Second)
	done := make(chan error, 1)
	go func() { _, err := reader.Get(context.Background(), "tenant", 7); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old read did not start")
	}
	fresh, err := reader.Get(authorizationrefresh.WithForce(context.Background()), "tenant", 7)
	if err != nil || fresh.ReportEnabled {
		t.Fatalf("fresh capability=%#v %v", fresh, err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old read did not finish")
	}
	options, err := reader.Get(context.Background(), "tenant", 7)
	if err != nil || options.ReportEnabled || calls.Load() != 2 {
		t.Fatalf("late read restored wide capability: %#v calls=%d error=%v", options, calls.Load(), err)
	}
}
