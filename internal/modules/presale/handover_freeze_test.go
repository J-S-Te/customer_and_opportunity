package presale

import (
	"context"
	"errors"
	"testing"
)

// allowAllFreezeChecker 是单测显式放行桩：交接冻结强制是 fail-closed，
// 没有显式接线时所有责任写入都会被拒绝，所以每个写路径用例都必须注入它。
type allowAllFreezeChecker struct{}

func (allowAllFreezeChecker) ActiveFreezeUser(context.Context, string, string) (bool, error) {
	return false, nil
}

type recordingFreezeChecker struct {
	frozen bool
	err    error
	calls  []string
}

func (checker *recordingFreezeChecker) ActiveFreezeUser(_ context.Context, _, userID string) (bool, error) {
	checker.calls = append(checker.calls, userID)
	return checker.frozen, checker.err
}

// 交接冻结中的用户不得接收新的售前责任。
func TestEnsureUserNotFrozenRejectsFrozenUser(t *testing.T) {
	checker := &recordingFreezeChecker{frozen: true}
	service := &Service{}
	service.UseFreezeChecker(checker)

	err := service.ensureUserNotFrozen(context.Background(), "tenant-1", "user-leaver")
	if !errors.Is(err, ErrUserFrozenByHandover) {
		t.Fatalf("err = %v, want ErrUserFrozenByHandover", err)
	}
	if len(checker.calls) != 1 {
		t.Fatalf("freeze lookups = %d, want 1", len(checker.calls))
	}
}

// 未接线检查器或空承接人一律拒绝（fail-closed），查询失败同样拒绝。
func TestEnsureUserNotFrozenFailsClosed(t *testing.T) {
	service := &Service{}
	service.UseFreezeChecker(allowAllFreezeChecker{})
	if err := service.ensureUserNotFrozen(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("unfrozen user must be accepted: %v", err)
	}

	unwired := &Service{}
	if err := unwired.ensureUserNotFrozen(context.Background(), "tenant-1", "user-1"); !errors.Is(err, ErrFreezeCheckerUnavailable) {
		t.Fatalf("unwired checker err = %v, want ErrFreezeCheckerUnavailable", err)
	}
	if unwired.HandoverFreezeEnforced() {
		t.Fatal("unwired service must not report freeze enforcement")
	}

	checker := &recordingFreezeChecker{}
	wired := &Service{}
	wired.UseFreezeChecker(checker)
	if !wired.HandoverFreezeEnforced() {
		t.Fatal("wired service must report freeze enforcement")
	}
	if err := wired.ensureUserNotFrozen(context.Background(), "tenant-1", "   "); !errors.Is(err, ErrFreezeSubjectRequired) {
		t.Fatalf("empty subject err = %v, want ErrFreezeSubjectRequired", err)
	}
	if len(checker.calls) != 0 {
		t.Fatalf("empty subject must not trigger a lookup, calls = %v", checker.calls)
	}

	failing := &Service{}
	failing.UseFreezeChecker(&recordingFreezeChecker{err: errors.New("freeze store unavailable")})
	if err := failing.ensureUserNotFrozen(context.Background(), "tenant-1", "user-1"); err == nil || errors.Is(err, ErrUserFrozenByHandover) {
		t.Fatalf("err = %v, want a lookup failure", err)
	}
}

// 生产仓储装配时若写路径缺少事务句柄，门闩无法生效，必须拒绝写入而不是静默放行。
func TestGuardUserWriteRequiresTransactionWhenDatabaseIsWired(t *testing.T) {
	service := &Service{db: nil}
	service.UseFreezeChecker(allowAllFreezeChecker{})
	if err := service.guardUserWrite(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("fixture without database must rely on the checker: %v", err)
	}

	unwired := &Service{}
	if err := unwired.guardUserWrite(context.Background(), "tenant-1", "user-1"); !errors.Is(err, ErrFreezeCheckerUnavailable) {
		t.Fatalf("unwired guard err = %v, want ErrFreezeCheckerUnavailable", err)
	}
}
