package ownerdirectory

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct{ catalog Catalog }

func NewHandler(catalog Catalog) *Handler { return &Handler{catalog: catalog} }

func (handler *Handler) List(c *gin.Context) {
	if handler == nil || handler.catalog == nil {
		response.Error(c, ErrUnavailable)
		return
	}
	page, err := optionalPositiveInt(c.Query("page"))
	if err != nil {
		response.Error(c, err)
		return
	}
	pageSize, err := optionalPositiveInt(c.Query("page_size"))
	if err != nil || pageSize > 50 {
		response.Error(c, apperror.New(422, "CRM_OWNER_DIRECTORY_QUERY_INVALID", "owner directory query is invalid"))
		return
	}
	result, err := handler.catalog.List(c.Request.Context(), Query{Keyword: c.Query("keyword"), UserID: c.Query("user_id"), Page: page, PageSize: pageSize})
	if err != nil {
		response.Error(c, normalizeError(err))
		return
	}
	response.OK(c, result)
}

func optionalPositiveInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, apperror.New(422, "CRM_OWNER_DIRECTORY_QUERY_INVALID", "owner directory query is invalid")
	}
	return parsed, nil
}
