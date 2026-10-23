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

// listOwnerDirectoryUsers 遵循平台 owner-directory 契约，每页最多
// 50 条。部门选择及校验必须使用同一个完整分页视图；
// 请求超大分页时，平台会正确地以 422 拒绝查询。
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

// resolveOwnerDisplayNames 从平台权威用户目录补全
// 用户名。CRM 令牌不要求可选的 OIDC name 声明，因此业务
// 快照及读取模型不得假设 Actor.UserName 始终存在。
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
