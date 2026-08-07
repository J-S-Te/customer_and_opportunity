package bootstrap

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

func TestPresaleActorUsesPlatformUserIDForInternalAssignmentIdentity(t *testing.T) {
	principal := auth.Principal{
		TenantID: "tenant-1",
		UserID:   "platform-user-1",
		PersonID: "legacy-external-person-9",
	}
	actor, err := (presaleActorResolver{}).Resolve(auth.WithPrincipal(context.Background(), principal))
	if err != nil {
		t.Fatal(err)
	}
	if actor.PersonID != principal.UserID {
		t.Fatalf("assignment identity=%q, want platform user id %q", actor.PersonID, principal.UserID)
	}
}
