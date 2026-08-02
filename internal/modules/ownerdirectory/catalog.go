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

type Catalog interface {
	List(context.Context, Query) (Page, error)
	Validate(context.Context, string, string) error
}

type UnavailableCatalog struct{}

func (UnavailableCatalog) List(context.Context, Query) (Page, error)      { return Page{}, ErrUnavailable }
func (UnavailableCatalog) Validate(context.Context, string, string) error { return ErrUnavailable }

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
