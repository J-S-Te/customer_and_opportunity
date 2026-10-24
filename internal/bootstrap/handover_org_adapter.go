package bootstrap

import (
	"context"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/handover"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

// handoverOwnerOrgResolver 解析接任人的权威主组织，
// 避免跨部门交接后 owner_org_id
// 仍指向前任的组织。
type handoverOwnerOrgResolver struct {
	catalog ownerdirectory.Catalog
}

var _ handover.OwnerOrgResolver = handoverOwnerOrgResolver{}

func (resolver handoverOwnerOrgResolver) PrimaryOrganization(ctx context.Context, tenantID, userID string) (string, error) {
	if resolver.catalog == nil {
		return "", ownerdirectory.ErrUnavailable
	}
	users, err := resolver.catalog.Resolve(ctx, []string{strings.TrimSpace(userID)})
	if err != nil {
		return "", err
	}
	user, ok := users[strings.TrimSpace(userID)]
	if !ok {
		return "", ownerdirectory.ErrSelectionInvalid
	}
	organizationID := ownerdirectory.PrimaryOrganization(ownerdirectory.Page{Items: []ownerdirectory.User{user}}, strings.TrimSpace(userID))
	if organizationID == "" {
		return "", ownerdirectory.ErrSelectionInvalid
	}
	return organizationID, nil
}
