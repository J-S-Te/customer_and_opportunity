package customer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type importOwnerResult struct {
	userID, orgID, displayName, orgName string
	code, message                       string
}
type importOwnerResolver struct {
	catalog ownerdirectory.Catalog
	actorID string
	result  *importOwnerResult
}

func newImportOwnerResolver(catalog ownerdirectory.Catalog, actorID string) *importOwnerResolver {
	return &importOwnerResolver{catalog: catalog, actorID: actorID}
}
func (r *importOwnerResolver) resolveRow(ctx context.Context, row *parsedImportRow, legacy bool) {
	if r.result == nil {
		result := resolveImportActor(ctx, r.catalog, r.actorID)
		r.result = &result
	}
	result := *r.result
	if result.code == "" && legacy && ((row.command.OwnerUserID != "" && row.command.OwnerUserID != result.userID) || (row.command.OwnerOrgID != "" && row.command.OwnerOrgID != result.orgID)) {
		result.code, result.message = "OWNER_ASSIGNMENT_NOT_ALLOWED", "导入客户统一归当前导入人及其主组织；请清空旧模板负责人 ID，导入后通过变更负责人分配"
	}
	if result.code != "" {
		row.issues = append(row.issues, ImportRowIssue{Column: "负责人", Code: result.code, Message: result.message})
		return
	}
	row.command.OwnerUserID, row.command.OwnerOrgID = result.userID, result.orgID
	row.preview.OwnerUserID, row.preview.OwnerDisplayName, row.preview.OwnerOrgName = result.userID, result.displayName, result.orgName
}

func resolveImportActor(ctx context.Context, catalog ownerdirectory.Catalog, actorID string) importOwnerResult {
	fail := func(code, message string) importOwnerResult { return importOwnerResult{code: code, message: message} }
	if catalog == nil {
		return fail("OWNER_DIRECTORY_UNAVAILABLE", "负责人目录暂不可用，请稍后重新预检")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	page, err := catalog.List(ctx, ownerdirectory.Query{UserID: actorID, Page: 1, PageSize: 2})
	if err != nil {
		return fail("OWNER_DIRECTORY_UNAVAILABLE", "负责人目录暂不可用，请稍后重新预检")
	}
	// The exact ID query must return the entire single-user result. Never select
	// the first item from an inconsistent or silently truncated response.
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != actorID {
		return fail("OWNER_SELECTION_INVALID", "当前导入人未在有效负责人目录中，请联系管理员检查账号及组织")
	}
	user := page.Items[0]
	var primary []ownerdirectory.Organization
	for _, org := range user.Organizations {
		if org.IsPrimary && strings.TrimSpace(org.ID) != "" {
			primary = append(primary, org)
		}
	}
	if len(primary) == 0 && len(user.Organizations) == 1 && strings.TrimSpace(user.Organizations[0].ID) != "" {
		primary = user.Organizations
	}
	if len(primary) != 1 {
		return fail("OWNER_ORGANIZATION_AMBIGUOUS", "当前导入人的主组织未唯一确定，请联系管理员配置主组织")
	}
	org := primary[0]
	if err = catalog.Validate(ctx, actorID, org.ID); err != nil {
		if errors.Is(err, ownerdirectory.ErrSelectionInvalid) {
			return fail("OWNER_SELECTION_INVALID", "当前导入人已失效或不属于其主组织，请重新预检")
		}
		return fail("OWNER_DIRECTORY_UNAVAILABLE", "负责人目录暂不可用，请稍后重新预检")
	}
	return importOwnerResult{userID: actorID, orgID: org.ID, displayName: user.DisplayName, orgName: org.Name}
}

func (s *Service) validateImportOwner(ctx context.Context, userID, orgID string) error {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return err
	}
	if userID != principal.UserID {
		return ownerdirectory.ErrSelectionInvalid
	}
	result := resolveImportActor(ctx, s.owners, principal.UserID)
	if result.code == "OWNER_DIRECTORY_UNAVAILABLE" {
		return ownerdirectory.ErrUnavailable
	}
	if result.code != "" || result.userID != userID || result.orgID != orgID {
		return ownerdirectory.ErrSelectionInvalid
	}
	return nil
}
