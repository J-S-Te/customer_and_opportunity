package bootstrap

import (
	"os"
	"strings"
	"testing"
)

func TestAuthMeResponseIncludesOrganizationClaims(t *testing.T) {
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, expected := range []string{
		`"primary_org_id": principal.PrimaryOrgID`,
		`"organization_ids": append([]string{}, principal.OrganizationIDs...)`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("auth/me response missing %s", expected)
		}
	}
}
