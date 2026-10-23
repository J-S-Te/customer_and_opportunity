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

// ParseQuery 校验列表端点通用的 page/page_size 查询
// 参数对。统一放在共享分页包中，防止处理器
// 在边界和默认语义上产生偏差。
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
