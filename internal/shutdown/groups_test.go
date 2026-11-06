package shutdown

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDrainTimeoutFallsBackToDefaultForInvalidOverride(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "unset", value: "", want: 20 * time.Second},
		{name: "seconds", value: "35", want: 35 * time.Second},
		{name: "padded", value: " 12 ", want: 12 * time.Second},
		{name: "zero", value: "0", want: 20 * time.Second},
		{name: "negative", value: "-5", want: 20 * time.Second},
		{name: "garbage", value: "30s", want: 20 * time.Second},
		{name: "fraction", value: "2.5", want: 20 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(DrainSecondsEnv, tc.value)
			if got := DrainTimeout(); got != tc.want {
				t.Fatalf("DrainTimeout()=%s want %s", got, tc.want)
			}
		})
	}
}

func TestGroupJoinReturnsComponentsStillRunningAfterBudget(t *testing.T) {
	group := NewGroup()
	released := make(chan struct{})
	exited := make(chan struct{})
	group.Go("request-audit-dispatcher", func() { <-released })
	group.Go("audit-publisher-preflight", func() { close(exited) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stuck := group.Join(ctx)
	if want := []string{"request-audit-dispatcher"}; !reflect.DeepEqual(stuck, want) {
		t.Fatalf("stuck=%v want %v", stuck, want)
	}
	close(released)
	if stuck = group.Join(context.Background()); len(stuck) != 0 {
		t.Fatalf("released components still reported: %v", stuck)
	}
}

func TestGroupJoinSortsEveryStuckComponent(t *testing.T) {
	group := NewGroup()
	block := make(chan struct{})
	defer close(block)
	for _, name := range []string{"worker-loop", "audit-publisher-preflight", "request-audit-dispatcher"} {
		group.Go(name, func() { <-block })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stuck := group.Join(ctx)
	want := []string{"audit-publisher-preflight", "request-audit-dispatcher", "worker-loop"}
	if !reflect.DeepEqual(stuck, want) {
		t.Fatalf("stuck=%v want %v", stuck, want)
	}
}

func TestRunWorkerPropagatesLoopError(t *testing.T) {
	want := errors.New("worker failed")
	if err := RunWorker(context.Background(), "worker-loop", func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("RunWorker()=%v want %v", err, want)
	}
}

func TestRunWorkerReturnsAfterSignalWhenLoopHonoursCancellation(t *testing.T) {
	t.Setenv(DrainSecondsEnv, "5")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- RunWorker(ctx, "worker-loop", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWorker()=%v want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWorker did not join a cancelled worker within budget")
	}
}

func TestRunWorkerReportsLoopThatOutlivesBudget(t *testing.T) {
	t.Setenv(DrainSecondsEnv, "1")
	ctx, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	defer close(block)
	errc := make(chan error, 1)
	go func() {
		errc <- RunWorker(ctx, "worker-loop", func(context.Context) error {
			<-block
			return nil
		})
	}()
	cancel()
	var err error
	select {
	case err = <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("RunWorker exceeded its budget without reporting stuck components")
	}
	var stuck *StuckError
	if !errors.As(err, &stuck) {
		t.Fatalf("RunWorker()=%v want *StuckError", err)
	}
	if want := []string{"worker-loop"}; !reflect.DeepEqual(stuck.Components, want) {
		t.Fatalf("components=%v want %v", stuck.Components, want)
	}
	if stuck.Budget != time.Second {
		t.Fatalf("budget=%s want 1s", stuck.Budget)
	}
}
