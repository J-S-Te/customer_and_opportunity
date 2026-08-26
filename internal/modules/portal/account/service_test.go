package account

import "testing"

func TestSafeReturnPath(t *testing.T) {
	tests := map[string]bool{"/projects": true, "/": true, "//evil.example": false, "https://evil.example": false, "/ok\r\nLocation: x": false}
	for value, want := range tests {
		if got := safeReturnPath(value); got != want {
			t.Errorf("safeReturnPath(%q)=%v want %v", value, got, want)
		}
	}
}

func TestHashDoesNotExposeSecret(t *testing.T) {
	if hash("secret") == "secret" || hash("secret") == "" {
		t.Fatal("hash must be opaque and non-empty")
	}
	if hash("secret") != hash("secret") {
		t.Fatal("hash must be deterministic")
	}
}

func TestPortalAuthorizationRejectsCrossApplicationClaims(t *testing.T) {
	valid := func(permissions []string) Claims {
		return Claims{Subject: "identity-a", Roles: []string{"portal_customer"}, Permissions: permissions, DataScopes: []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}}
	}
	if !validPortalAuthorization(valid([]string{"project.read", "report.read"}), "dev") {
		t.Fatal("valid Portal catalog must be accepted")
	}
	if !validPortalAuthorization(valid([]string{"evaluation.create", "evaluation.read"}), "dev") {
		t.Fatal("evaluation permissions must be accepted by the Portal catalog")
	}
	if !validPortalAuthorization(valid([]string{"filing.read", "filing.create", "filing.update", "filing.submit"}), "dev") {
		t.Fatal("filing permissions must be accepted by the Portal catalog")
	}
	if !validPortalAuthorization(valid([]string{"feedback.create", "feedback.read", "feedback.reply"}), "dev") {
		t.Fatal("feedback permissions must be accepted by the Portal catalog")
	}
	for _, test := range []Claims{
		{Subject: "identity-a", Roles: []string{"portal_customer", "crm_admin"}, Permissions: []string{"project.read"}, DataScopes: []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION"}}},
		valid([]string{"contract.read"}),
		valid(nil),
	} {
		if validPortalAuthorization(test, "dev") {
			t.Fatalf("cross-application authorization accepted: %#v", test)
		}
	}
}

func TestPortalAuthorizationScopeSemantics(t *testing.T) {
	base := Claims{Subject: "identity-a", PersonID: "person-a", Roles: []string{"portal_customer"}, Permissions: []string{"project.read"}}
	for _, scope := range []DataScope{
		{RoleCode: "portal_customer", ScopeType: "APPLICATION"},
		{RoleCode: "portal_customer", ScopeType: "TENANT"},
		{RoleCode: "portal_customer", ScopeType: "ENVIRONMENT", ScopeID: "01ENV", EnvironmentCode: "dev"},
		{RoleCode: "portal_customer", ScopeType: "SELF", ScopeID: "person-a", EnvironmentCode: "dev"},
	} {
		claims := base
		claims.DataScopes = []DataScope{scope}
		if !validPortalAuthorization(claims, "dev") {
			t.Fatalf("valid scope rejected: %#v", scope)
		}
	}
	base.DataScopes = []DataScope{{RoleCode: "portal_customer", ScopeType: "APPLICATION", ScopeID: "unexpected"}}
	if validPortalAuthorization(base, "dev") {
		t.Fatal("APPLICATION scope with scope_id was accepted")
	}
	for _, scope := range []DataScope{
		{RoleCode: "portal_customer", ScopeType: "ORGANIZATION", ScopeID: "org-a"},
		{RoleCode: "portal_customer", ScopeType: "PROJECT", ScopeID: "project-a"},
	} {
		base.DataScopes = []DataScope{scope}
		if validPortalAuthorization(base, "dev") {
			t.Fatalf("scope without a safe customer binding was accepted: %#v", scope)
		}
	}
}

func TestPortalSuperAdminAuthorizationRequiresGlobalScope(t *testing.T) {
	base := Claims{Subject: "identity-a", Roles: []string{"portal_super_admin"}, Permissions: []string{"project.read"}}
	for _, scope := range []DataScope{
		{RoleCode: "portal_super_admin", ScopeType: "APPLICATION"},
		{RoleCode: "portal_super_admin", ScopeType: "TENANT"},
		{RoleCode: "portal_super_admin", ScopeType: "ENVIRONMENT", ScopeID: "01ENV", EnvironmentCode: "dev"},
	} {
		claims := base
		claims.DataScopes = []DataScope{scope}
		if !validPortalAuthorization(claims, "dev") {
			t.Fatalf("valid admin scope rejected: %#v", scope)
		}
	}
	for _, scope := range []DataScope{
		{RoleCode: "portal_super_admin", ScopeType: "SELF", ScopeID: "identity-a", EnvironmentCode: "dev"},
		{RoleCode: "portal_super_admin", ScopeType: "PROJECT", ScopeID: "project-a"},
	} {
		claims := base
		claims.DataScopes = []DataScope{scope}
		if validPortalAuthorization(claims, "dev") {
			t.Fatalf("customer-side admin scope accepted: %#v", scope)
		}
	}
}

func TestPortalAccountIDIsStableBusinessIdentifier(t *testing.T) {
	if got := portalAccountID(42); got != "PA42" {
		t.Fatalf("portalAccountID(42)=%q", got)
	}
}
