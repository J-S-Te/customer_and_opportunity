package customer

import (
	"context"
	"errors"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
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

// 交接冻结中的用户不得成为新客户负责人。
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

// 未冻结放行；未接线或空负责人不查询（向后兼容）。
func TestEnsureOwnerNotFrozenAllowsAndSkips(t *testing.T) {
	service := &Service{}
	service.UseFreezeChecker(&freezeCheckerStub{frozen: false})
	if err := service.ensureOwnerNotFrozen(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("unfrozen owner must be accepted: %v", err)
	}
	unwired := &Service{}
	if err := unwired.ensureOwnerNotFrozen(context.Background(), "tenant-1", "user-1"); err != nil {
		t.Fatalf("unwired checker must not block: %v", err)
	}
	checker := &freezeCheckerStub{frozen: true}
	wired := &Service{}
	wired.UseFreezeChecker(checker)
	if err := wired.ensureOwnerNotFrozen(context.Background(), "tenant-1", "   "); err != nil {
		t.Fatalf("empty owner must be skipped: %v", err)
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

// 导入路径也必须拒绝交接中的负责人（预检通过不等于可写入）。
func TestImportCommitRejectsFrozenOwner(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "a"})
	service := &Service{owners: importTestCatalog()}
	service.UseFreezeChecker(&freezeCheckerStub{frozen: true})
	if err := service.validateImportOwner(ctx, "a", "sales"); !errors.Is(err, ErrOwnerFrozenByHandover) {
		t.Fatalf("frozen owner import err = %v, want ErrOwnerFrozenByHandover", err)
	}
	service.UseFreezeChecker(&freezeCheckerStub{frozen: false})
	if err := service.validateImportOwner(ctx, "a", "sales"); err != nil {
		t.Fatalf("unfrozen owner import must pass: %v", err)
	}
}
