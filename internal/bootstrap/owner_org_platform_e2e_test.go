package bootstrap

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

func TestOwnerOrgResolverAgainstRealPlatformDirectory(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("OWNER_DIRECTORY_E2E_ENDPOINT"))
	tokenURL := strings.TrimSpace(os.Getenv("OWNER_DIRECTORY_E2E_TOKEN_URL"))
	clientID := strings.TrimSpace(os.Getenv("OWNER_DIRECTORY_E2E_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("OWNER_DIRECTORY_E2E_CLIENT_SECRET"))
	tenantID := strings.TrimSpace(os.Getenv("OWNER_DIRECTORY_E2E_TENANT_ID"))
	if endpoint == "" || tokenURL == "" || clientID == "" || clientSecret == "" || tenantID == "" {
		t.Skip("real owner-directory E2E environment is not configured")
	}
	client, err := ownerdirectory.NewHTTPClient(context.Background(), ownerdirectory.HTTPOptions{
		Endpoint: endpoint, TokenURL: tokenURL, ClientID: clientID, ClientSecret: clientSecret, Scope: "owner_directory.read",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.List(context.Background(), ownerdirectory.Query{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("real platform owner-directory list: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("real platform owner-directory returned no users")
	}
	var selected ownerdirectory.User
	for _, user := range page.Items {
		if len(user.Organizations) > 0 && strings.TrimSpace(user.ID) != "" {
			selected = user
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("no platform user with an organization")
	}
	resolver := handoverOwnerOrgResolver{catalog: client}
	organizationID, err := resolver.PrimaryOrganization(context.Background(), tenantID, selected.ID)
	if err != nil || strings.TrimSpace(organizationID) == "" {
		t.Fatalf("resolver user=%s org=%q err=%v", selected.ID, organizationID, err)
	}
	found := false
	for _, organization := range selected.Organizations {
		if organization.ID == organizationID {
			found = true
		}
	}
	if !found {
		t.Fatalf("resolver returned org=%q outside platform user organizations", organizationID)
	}
}
