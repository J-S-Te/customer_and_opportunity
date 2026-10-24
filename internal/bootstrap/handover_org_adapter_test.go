package bootstrap

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type handoverOrgCatalog struct {
	users map[string]ownerdirectory.User
	err   error
}

func (catalog handoverOrgCatalog) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	return ownerdirectory.Page{}, catalog.err
}
func (catalog handoverOrgCatalog) Validate(context.Context, string, string) error { return catalog.err }
func (catalog handoverOrgCatalog) Resolve(_ context.Context, ids []string) (map[string]ownerdirectory.User, error) {
	if catalog.err != nil {
		return nil, catalog.err
	}
	result := map[string]ownerdirectory.User{}
	for _, id := range ids {
		if user, ok := catalog.users[id]; ok {
			result[id] = user
		}
	}
	return result, nil
}

func TestHandoverOwnerOrgResolverUsesPrimaryOrganization(t *testing.T) {
	catalog := handoverOrgCatalog{users: map[string]ownerdirectory.User{
		"user-new": {ID: "user-new", Organizations: []ownerdirectory.Organization{{ID: "org-primary", IsPrimary: true}, {ID: "org-secondary"}}},
	}}
	resolver := handoverOwnerOrgResolver{catalog: catalog}
	if org, err := resolver.PrimaryOrganization(context.Background(), "tenant-1", "user-new"); err != nil || org != "org-primary" {
		t.Fatalf("org=%q err=%v", org, err)
	}
	if _, err := resolver.PrimaryOrganization(context.Background(), "tenant-1", "missing"); err == nil {
		t.Fatal("missing owner must fail closed")
	}
}
