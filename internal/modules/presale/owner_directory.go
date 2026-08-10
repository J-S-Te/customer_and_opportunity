package presale

import (
	"context"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

const (
	ownerDirectoryPageSize = 50
	ownerDirectoryMaxPages = 200
)

// listOwnerDirectoryUsers follows the platform owner-directory contract, whose maximum page
// size is 50. Department selection and validation must use the same complete, paginated view;
// requesting an oversized page makes the platform correctly reject the query with 422.
func listOwnerDirectoryUsers(ctx context.Context, catalog ownerdirectory.Catalog) ([]ownerdirectory.User, error) {
	result := make([]ownerdirectory.User, 0)
	for pageNumber := 1; pageNumber <= ownerDirectoryMaxPages; pageNumber++ {
		page, err := catalog.List(ctx, ownerdirectory.Query{Page: pageNumber, PageSize: ownerDirectoryPageSize})
		if err != nil {
			return nil, err
		}
		result = append(result, page.Items...)
		if len(page.Items) == 0 || int64(len(result)) >= page.Total || len(page.Items) < ownerDirectoryPageSize {
			return result, nil
		}
	}
	return nil, ownerdirectory.ErrUnavailable
}

// resolveOwnerDisplayNames fills user names from the platform's authoritative user
// directory. CRM tokens do not require the optional OIDC name claim, so business
// snapshots and read models must not assume that Actor.UserName is always present.
func resolveOwnerDisplayNames(ctx context.Context, catalog ownerdirectory.Catalog, userIDs []string) (map[string]string, error) {
	names := make(map[string]string)
	if catalog == nil {
		return names, nil
	}
	resolved, err := catalog.Resolve(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	for userID, user := range resolved {
		if name := strings.TrimSpace(user.DisplayName); name != "" {
			names[userID] = name
		}
	}
	return names, nil
}
