// Package shutdown 提供本仓库独立的最小有界停机收敛：登记中后台组件、按预算 join，
// 并在预算耗尽时按稳定顺序报告仍未退出的组件名。
//
// 本包只做进程生命周期编排，不含业务语义，也不跨仓库引用平台实现。
package shutdown

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultDrainSeconds 是取消后台组件后等待其退出的默认预算（秒）。
	DefaultDrainSeconds = 20
	// DrainSecondsEnv 可覆盖默认预算；取值 <=0、非整数或带空白后缀等非法输入一律回落默认值，
	// 避免错误配置把停机预算变成 0 或负数后立即判定「未退出」。
	DrainSecondsEnv = "CUSTOMER_OPPORTUNITY_SHUTDOWN_DRAIN_SECONDS"

	// HTTPServeTimeout 是 HTTP 服务等待在途请求收敛的既有预算（10s）。
	HTTPServeTimeout = 10 * time.Second
	// BackgroundJoinTimeout 是取消后台组件后 join 审计投递器、导入源与预检的既有预算（10s）；
	// 它固定小于整体停机预算，避免投递器长时间占用停机窗口。
	BackgroundJoinTimeout = 10 * time.Second
)

// DrainTimeout 解析停机 join 预算：默认 20s，可由 CUSTOMER_OPPORTUNITY_SHUTDOWN_DRAIN_SECONDS 覆盖。
// 返回值：合法的正整数秒，或默认预算；非法输入不报错，回落到默认预算。
func DrainTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(DrainSecondsEnv))
	if raw == "" {
		return DefaultDrainSeconds * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return DefaultDrainSeconds * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// Group 跟踪一组中后台 goroutine，并提供有界 join。
// 零值不可用，必须通过 NewGroup 构造；Group 的方法可并发调用。
type Group struct {
	mu      sync.Mutex
	members []*member
}

type member struct {
	name string
	done chan struct{}
	once sync.Once
}

// NewGroup 返回空的后台组件集合。
func NewGroup() *Group { return &Group{} }

// Go 登记并启动一个后台组件；组件函数返回即视为已退出。
// 参数：name 为停机告警中展示的稳定组件名；run 为组件主循环。
func (g *Group) Go(name string, run func()) {
	done := make(chan struct{})
	g.mu.Lock()
	g.members = append(g.members, &member{name: name, done: done})
	g.mu.Unlock()
	go func() {
		defer close(done)
		run()
	}()
}

// Join 在 ctx 到期前等待所有已登记组件退出，返回仍未退出的组件名（按字典序排序）。
// 参数：ctx 为 join 预算；调用方负责在返回非空名单时记录日志并以非零码退出。
// 返回值：未退出组件名；全部退出时为空切片。
func (g *Group) Join(ctx context.Context) []string {
	g.mu.Lock()
	members := make([]*member, len(g.members))
	copy(members, g.members)
	g.mu.Unlock()

	var stuck []string
	for _, item := range members {
		select {
		case <-item.done:
			continue
		default:
		}
		select {
		case <-item.done:
		case <-ctx.Done():
			stuck = append(stuck, item.name)
		}
	}
	sort.Strings(stuck)
	return stuck
}

// StuckError 表示停机预算耗尽后仍有组件在运行。
// 它只携带组件名与预算，不含业务数据或连接信息。
type StuckError struct {
	Budget     time.Duration
	Components []string
}

func (e *StuckError) Error() string {
	return fmt.Sprintf("shutdown drain budget %s exceeded; components still running: %s", e.Budget, strings.Join(e.Components, ", "))
}

// RunWorker 运行单循环 worker 主循环，并在 ctx 取消后按预算 join 该循环。
// 参数：name 为停机告警中展示的组件名；run 为 worker 主循环。
// 返回值：主循环错误；主循环超预算未退出时返回 *StuckError（含未退出组件名），由调用方非零退出。
func RunWorker(ctx context.Context, name string, run func(context.Context) error) error {
	group := NewGroup()
	done := make(chan error, 1)
	group.Go(name, func() { done <- run(ctx) })
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	budget := DrainTimeout()
	drainCtx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if stuck := group.Join(drainCtx); len(stuck) > 0 {
		return &StuckError{Budget: budget, Components: stuck}
	}
	return <-done
}
