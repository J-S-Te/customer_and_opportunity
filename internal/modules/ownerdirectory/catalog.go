package ownerdirectory

import (
	"context"
	"errors"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	ErrSelectionInvalid = apperror.New(422, "CRM_OWNER_SELECTION_INVALID", "owner and organization must be an active authorized platform membership")
	ErrUnavailable      = apperror.New(503, "CRM_OWNER_DIRECTORY_UNAVAILABLE", "platform owner directory is unavailable")
)

type Organization struct {
	ID        string `json:"organization_id"`
	Name      string `json:"organization_name"`
	IsPrimary bool   `json:"is_primary"`
}

type User struct {
	ID            string         `json:"user_id"`
	DisplayName   string         `json:"display_name"`
	Organizations []Organization `json:"organizations"`
}

type Page struct {
	Items    []User `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int64  `json:"total"`
}

type Query struct {
	Keyword   string
	UserID    string
	RoleCodes []string
	Page      int
	PageSize  int
}

// PrimaryOrganization 返回权威平台目录标记的
// 用户主组织。此处仅考虑
// 目录中与该用户精确匹配的条目。
func PrimaryOrganization(page Page, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	for _, user := range page.Items {
		if strings.TrimSpace(user.ID) != userID {
			continue
		}
		for _, organization := range user.Organizations {
			if organization.IsPrimary && strings.TrimSpace(organization.ID) != "" {
				return strings.TrimSpace(organization.ID)
			}
		}
		// 旧版目录响应可能仅包含一个有效组织，
		// 但没有主组织标记。仍可安全使用，
		// 因为该组织由权威目录在此精确用户条目下返回。
		if len(user.Organizations) == 1 {
			return strings.TrimSpace(user.Organizations[0].ID)
		}
	}
	return ""
}

type Catalog interface {
	List(context.Context, Query) (Page, error)
	Validate(context.Context, string, string) error
	Resolve(context.Context, []string) (map[string]User, error)
}

type UnavailableCatalog struct{}

func (UnavailableCatalog) List(context.Context, Query) (Page, error)      { return Page{}, ErrUnavailable }
func (UnavailableCatalog) Validate(context.Context, string, string) error { return ErrUnavailable }
func (UnavailableCatalog) Resolve(context.Context, []string) (map[string]User, error) {
	return nil, ErrUnavailable
}

func validatePair(page Page, userID, organizationID string) error {
	userID, organizationID = strings.TrimSpace(userID), strings.TrimSpace(organizationID)
	if userID == "" || organizationID == "" {
		return ErrSelectionInvalid
	}
	for _, user := range page.Items {
		if user.ID != userID {
			continue
		}
		for _, organization := range user.Organizations {
			// 只有平台目录在同一个用户条目下返回的组织才构成有效负责人归属；
			// 不能把两个各自存在但没有任职关系的标识拼成一对。
			if organization.ID == organizationID {
				return nil
			}
		}
	}
	return ErrSelectionInvalid
}

func normalizeError(err error) error {
	if err == nil || errors.Is(err, ErrSelectionInvalid) || errors.Is(err, ErrUnavailable) {
		return err
	}
	return ErrUnavailable
}
