package portalinvitecompensationworker

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
	"testing"
)

func TestExpiredCompensationBlocksProvisioningButCompletesDisable(t *testing.T) {
	ctx := workerlicense.WithCheck(context.Background(), func(_ context.Context, op core.Operation) error {
		if op == core.MUTATE_BUSINESS {
			return errors.New("expired")
		}
		return nil
	})
	store := &fakeStore{tasks: []portalinvite.CompensationTask{validTask(portalinvite.CompensationRole), validTask(portalinvite.CompensationMapping), validTask(portalinvite.CompensationBinding), validTask(portalinvite.CompensationBindingDisable)}}
	repair := &fakeBindingRepair{}
	reconciler := &fakeIdentityReconciler{}
	w := NewWorker(store, fakeRoles{}, fakeMappings{}, testConfig()).withBindingRepair(repair).withReconciler(reconciler, 1)
	_, err := w.RunOnce(ctx)
	if !errors.Is(err, workerlicense.ErrDenied) || repair.calls != 1 || store.completedBinding != 1 || store.completedRole != 0 || reconciler.calls != 0 {
		t.Fatal("expired mixed worker advanced business or blocked safety")
	}
}
