package opportunity

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// commercialRecordService 用途：按现有主服务连接组装台账，不引入新的全局依赖或配置。
// 参数：无。
// 返回值：人工资料服务；未完成数据库接线时返回不可用错误。
func (h *Handler) commercialRecordService() (*CommercialRecordService, error) {
	if h == nil || h.service == nil || h.service.db == nil || h.service.repo == nil || h.service.audit == nil {
		return nil, apperror.New(503, "CRM_OPPORTUNITY_COMMERCIAL_RECORD_UNAVAILABLE", "人工资料服务未配置")
	}
	service := NewCommercialRecordService(NewGORMCommercialRecordRepository(h.service.db), h.service.repo, h.service.audit)
	if h.service.now != nil {
		service.now = h.service.now
	}
	return service, nil
}

// commercialRecordOpportunityID 用途：严格校验人工资料接口的父路径和查询参数。
// 参数：c 为 HTTP 上下文，list 表示允许分页参数。
// 返回值：父商机编号；无效路径、重复或未知查询参数返回错误。
func commercialRecordOpportunityID(c *gin.Context, list bool) (uint64, error) {
	allowed := []string{}
	if list {
		allowed = []string{"page", "page_size"}
	}
	if !validQueryKeys(c, allowed...) {
		return 0, ErrInvalidQuery
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, invalid(err)
	}
	if !list && (len(c.Param("recordID")) > 64) {
		return 0, invalid(nil)
	}
	return id, nil
}

// ListCommercialRecords 用途：分页返回父商机的人工报价及投标资料。
// 参数：c 为通过商机读取权限中间件的 HTTP 上下文。
// 返回值：无；输出现有成功或错误信封。
func (h *Handler) ListCommercialRecords(c *gin.Context) {
	id, err := commercialRecordOpportunityID(c, true)
	if err != nil {
		response.Error(c, err)
		return
	}
	page, err := parsePositiveQueryInt(c, "page", 1, maxQueryPage)
	if err != nil {
		response.Error(c, err)
		return
	}
	size, err := parsePositiveQueryInt(c, "page_size", 20, 100)
	if err != nil {
		response.Error(c, err)
		return
	}
	service, err := h.commercialRecordService()
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := service.List(c.Request.Context(), id, page, size)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

// CreateCommercialRecord 用途：登记人工资料并绑定必填幂等键。
// 参数：c 为通过商机更新权限中间件的 HTTP 上下文。
// 返回值：无；成功输出 201 资料信封，失败输出现有错误信封。
func (h *Handler) CreateCommercialRecord(c *gin.Context) {
	id, err := commercialRecordOpportunityID(c, false)
	if err != nil {
		response.Error(c, err)
		return
	}
	var input CommercialRecordRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, requestbody.InvalidArgument(err, &input))
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	service, err := h.commercialRecordService()
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := service.Create(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, value)
}

// UpdateCommercialRecord 用途：按资料自身版本更正人工登记，不更新父商机版本。
// 参数：c 为通过商机更新权限中间件的 HTTP 上下文。
// 返回值：无；成功输出 200 新版本，失败输出现有错误信封。
func (h *Handler) UpdateCommercialRecord(c *gin.Context) {
	id, err := commercialRecordOpportunityID(c, false)
	if err != nil {
		response.Error(c, err)
		return
	}
	var input CommercialRecordRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, requestbody.InvalidArgument(err, &input))
		return
	}
	service, err := h.commercialRecordService()
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := service.Update(c.Request.Context(), id, c.Param("recordID"), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

// VoidCommercialRecord 用途：保留资料并登记作废原因，拒绝重复消耗版本。
// 参数：c 为通过商机更新权限中间件的 HTTP 上下文。
// 返回值：无；成功输出 200 作废资料，失败输出现有错误信封。
func (h *Handler) VoidCommercialRecord(c *gin.Context) {
	id, err := commercialRecordOpportunityID(c, false)
	if err != nil {
		response.Error(c, err)
		return
	}
	var input CommercialRecordVoidRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, requestbody.InvalidArgument(err, &input))
		return
	}
	service, err := h.commercialRecordService()
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := service.Void(c.Request.Context(), id, c.Param("recordID"), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}
