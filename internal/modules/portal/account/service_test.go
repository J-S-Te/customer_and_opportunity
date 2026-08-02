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
	if !validPortalAuthorization([]string{"portal_customer"}, []string{"project.read", "report.read"}) {
		t.Fatal("valid Portal catalog must be accepted")
	}
	if !validPortalAuthorization([]string{"portal_customer"}, []string{"evaluation.create", "evaluation.read"}) {
		t.Fatal("evaluation permissions must be accepted by the Portal catalog")
	}
	if !validPortalAuthorization([]string{"portal_customer"}, []string{"filing.read", "filing.create", "filing.update", "filing.submit"}) {
		t.Fatal("filing permissions must be accepted by the Portal catalog")
	}
	if !validPortalAuthorization([]string{"portal_customer"}, []string{"feedback.create", "feedback.read", "feedback.reply"}) {
		t.Fatal("feedback permissions must be accepted by the Portal catalog")
	}
	for _, test := range []struct{ roles, permissions []string }{
		{[]string{"portal_customer", "crm_admin"}, []string{"project.read"}},
		{[]string{"portal_customer"}, []string{"contract.read"}},
		{[]string{"portal_customer"}, nil},
	} {
		if validPortalAuthorization(test.roles, test.permissions) {
			t.Fatalf("cross-application authorization accepted: %#v", test)
		}
	}
}

func TestPortalAccountIDIsStableBusinessIdentifier(t *testing.T) {
	if got := portalAccountID(42); got != "PA42" {
		t.Fatalf("portalAccountID(42)=%q", got)
	}
}
