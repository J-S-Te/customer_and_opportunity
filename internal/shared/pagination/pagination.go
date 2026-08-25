package pagination

import (
	"errors"
	"strconv"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

type Page[T any] struct {
	Items    []T   `json:"items"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
}

func Normalize(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}

// ParseQuery validates the common page/page_size query pair used by list
// endpoints. Keeping this in the shared pagination package prevents handlers
// from drifting on bounds and default semantics.
func ParseQuery(pageRaw, pageSizeRaw string) (int, int, error) {
	page, err := strconv.Atoi(pageRaw)
	if err != nil || page < 1 {
		return 0, 0, errors.New("invalid page")
	}
	pageSize, err := strconv.Atoi(pageSizeRaw)
	if err != nil || pageSize < 1 || pageSize > MaxPageSize {
		return 0, 0, errors.New("invalid page size")
	}
	return page, pageSize, nil
}
