package opportunity

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// WinConfirmation 返回已通过商机数据范围校验的本地赢单依据。
// 参数：c 为已认证且拥有商机读取权限的 HTTP 请求。
// 返回值：当前模式与历史证据，错误使用既有统一响应。
func (h *Handler) WinConfirmation(c *gin.Context) {
	id, err := attachmentOpportunityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := h.service.GetWinConfirmation(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}
