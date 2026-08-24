package bootstrap

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
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

func TestPresaleOpportunityPrincipalPreservesSignedDataScope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		scope string
		want  auth.ScopeMode
	}{
		{name: "self", scope: string(auth.ScopeSelf), want: auth.ScopeSelf},
		{name: "organization", scope: string(auth.ScopeOrg), want: auth.ScopeOrg},
		{name: "all", scope: string(auth.ScopeAll), want: auth.ScopeAll},
		{name: "unknown fails closed", scope: "UNKNOWN", want: auth.ScopeSelf},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := presale.Actor{
				TenantID: "tenant-1", UserID: "user-1", PersonID: "person-1",
				ScopeMode: test.scope, OrganizationIDs: []string{"org-a", "org-b"},
				Permissions: map[string]bool{"presale.create": true, "denied": false},
				Roles:       map[string]bool{"sales_director": true},
			}
			principal := presaleOpportunityPrincipal(actor)
			if principal.ScopeMode != test.want {
				t.Fatalf("scope=%q, want %q", principal.ScopeMode, test.want)
			}
			if len(principal.OrganizationIDs) != 2 || principal.OrganizationIDs[0] != "org-a" {
				t.Fatalf("organization ids=%v", principal.OrganizationIDs)
			}
			if _, ok := principal.Permissions["presale.create"]; !ok {
				t.Fatal("allowed permission was not preserved")
			}
			if _, ok := principal.Permissions["denied"]; ok {
				t.Fatal("denied permission must not be copied")
			}
			actor.OrganizationIDs[0] = "mutated"
			if principal.OrganizationIDs[0] != "org-a" {
				t.Fatal("principal organization ids alias actor storage")
			}
		})
	}
}
