package auth

import "testing"

func TestHasPermissionOrRoleUsesExplicitApprovalFallback(t *testing.T) {
	tests := []struct {
		name        string
		principal   Principal
		wantAllowed bool
	}{
		{name: "permission", principal: Principal{Permissions: map[string]struct{}{"customer.credit.approve": {}}}, wantAllowed: true},
		{name: "sales director legacy session", principal: Principal{Roles: []string{"sales_director"}}, wantAllowed: true},
		{name: "crm super admin legacy session", principal: Principal{Roles: []string{"crm_super_admin"}}, wantAllowed: true},
		{name: "unrelated role", principal: Principal{Roles: []string{"sales"}}, wantAllowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allowed := HasPermissionOrRole(test.principal, "customer.credit.approve", "sales_director", "crm_super_admin")
			if allowed != test.wantAllowed {
				t.Fatalf("allowed=%v want=%v", allowed, test.wantAllowed)
			}
		})
	}
}
