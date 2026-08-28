package authorizationcontext

import "testing"

func TestRoleConfigCompatibilityAcceptsNMinusOneAndRejectsPartialWindow(t *testing.T) {
	response := Response{CatalogVersion: "3", CompatibleCatalogVersions: []string{"3", "2"}, RoleConfigHash: "hash-3", CompatibleRoleConfigHashes: []string{"hash-3", "hash-2"}}
	if err := validateRoleConfigCompatibility(response, "hash-2"); err != nil {
		t.Fatalf("N-1 role hash rejected: %v", err)
	}
	response.CompatibleCatalogVersions = nil
	if err := validateRoleConfigCompatibility(response, "hash-2"); err == nil {
		t.Fatal("partial compatibility response accepted")
	}
}
