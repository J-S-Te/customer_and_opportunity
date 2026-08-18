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
	Keyword  string
	UserID   string
	Page     int
	PageSize int
}

// PrimaryOrganization returns the organization that the authoritative platform
// directory marks as the user's primary organization. It deliberately only
// considers the matching user entry returned by the directory.
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
		// Older directory responses may contain exactly one active organization
		// without the primary marker. It is still safe to use because it is
		// returned under this exact user entry by the authoritative directory.
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
