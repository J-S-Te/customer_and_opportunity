package bootstrap

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// 组合商机与售前两个公开服务边界：先证明调用方可见该商机，再由售前服务应用独立的 TS-007
// 角色范围，避免只凭售前权限枚举其他组织的商机。
type opportunityPresaleHandler struct {
	opportunities *opportunity.Service
	presales      *presale.Service
	actors        presaleActorResolver
}

func (h opportunityPresaleHandler) List(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_OPPORTUNITY_INVALID_ID", "invalid opportunity id"))
		return
	}
	if !onlyOpportunityPresaleQueryKeys(c) {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page"))
		return
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page size"))
		return
	}
	if _, err = h.opportunities.Get(c.Request.Context(), id); err != nil {
		response.Error(c, err)
		return
	}
	actor, err := h.actors.Resolve(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	result, err := h.presales.ListForOpportunity(c.Request.Context(), actor, id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func onlyOpportunityPresaleQueryKeys(c *gin.Context) bool {
	valuesByKey, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range valuesByKey {
		if (key != "page" && key != "page_size") || len(values) != 1 {
			return false
		}
	}
	return true
}
