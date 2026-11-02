package opportunity

import (
	"context"
	"errors"
	"testing"
)

type freezeCheckerStub struct {
	frozen bool
	err    error
	calls  int
}

func (stub *freezeCheckerStub) ActiveFreezeUser(_ context.Context, _, _ string) (bool, error) {
	stub.calls++
	return stub.frozen, stub.err
}

// allowAllFreezeChecker 是单测显式放行桩：交接冻结强制是 fail-closed，
// 没有显式接线时所有责任写入都会被拒绝，所以每个写路径用例都必须注入它。
type allowAllFreezeChecker struct{}

func (allowAllFreezeChecker) ActiveFreezeUser(context.Context, string, string) (bool, error) {
	return false, nil
}

// 交接冻结中的用户不得成为新商机负责人。
func TestEnsureOwnerNotFrozenRejectsFrozenOwner(t *testing.T) {
	checker := &freezeCheckerStub{frozen: true}
	service := &Service{}
	service.UseFreezeChecker(checker)

	err := service.ensureOwnerNotFrozen(context.Background(), "tenant-1", "user-leaver")
	if !errors.Is(err, ErrOwnerFrozenByHandover) {
		t.Fatalf("err = %v, want ErrOwnerFrozenByHandover", err)
	}
	if checker.calls != 1 {
		t.Fatalf("freeze lookups = %d, want 1", checker.calls)
	}
}

// 未冻结放行；未接线检查器或空负责人一律拒绝（fail-closed，不再静默放行）。
func TestEnsureOwnerNotFrozenFailsClosedWithoutChecker(t *testing.T) {
	service := &Service{}
	service.UseFreezeChecker(allowAllFreezeChecker{})
	if err := service.ensureOwnerNotFrozen(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("unfrozen owner must be accepted: %v", err)
	}

	unwired := &Service{}
	if err := unwired.ensureOwnerNotFrozen(context.Background(), "tenant-1", "user-1"); !errors.Is(err, ErrFreezeCheckerUnavailable) {
		t.Fatalf("unwired checker err = %v, want ErrFreezeCheckerUnavailable", err)
	}
	if unwired.HandoverFreezeEnforced() {
		t.Fatal("unwired service must not report freeze enforcement")
	}

	checker := &freezeCheckerStub{frozen: false}
	wired := &Service{}
	wired.UseFreezeChecker(checker)
	if !wired.HandoverFreezeEnforced() {
		t.Fatal("wired service must report freeze enforcement")
	}
	if err := wired.ensureOwnerNotFrozen(context.Background(), "tenant-1", "   "); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("empty owner err = %v, want ErrOwnerRequired", err)
	}
	if checker.calls != 0 {
		t.Fatalf("empty owner must not trigger a lookup, calls = %d", checker.calls)
	}
}

// 查询失败必须 失败时安全拒绝 上报，不能当作未冻结放行。
func TestEnsureOwnerNotFrozenReportsLookupFailure(t *testing.T) {
	service := &Service{}
	service.UseFreezeChecker(&freezeCheckerStub{err: errors.New("freeze store unavailable")})
	err := service.ensureOwnerNotFrozen(context.Background(), "tenant-1", "user-1")
	if err == nil || errors.Is(err, ErrOwnerFrozenByHandover) {
		t.Fatalf("err = %v, want a lookup failure", err)
	}
}
