package customer

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

const importMultipartBodyLimit = (10 << 20) + (64 << 10)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) Create(c *gin.Context) {
	var input CreateRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	result, err := h.service.Create(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

func (h *Handler) CheckDuplicate(c *gin.Context) {
	var input DuplicateCheckRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.CheckDuplicate(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) Merge(c *gin.Context) {
	var input MergeRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	result, err := h.service.Merge(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return
	}
	result, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) List(c *gin.Context) {
	if !validCustomerListKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	page, err := positiveQueryInt(c.DefaultQuery("page", "1"), 0)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	pageSize, err := positiveQueryInt(c.DefaultQuery("page_size", "20"), 100)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	createdFrom, err := parseOptionalTime(c.Query("created_from"))
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	createdTo, err := parseOptionalTime(c.Query("created_to"))
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	lastFollowupFrom, err := parseOptionalTime(c.Query("last_followup_from"))
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	lastFollowupTo, err := parseOptionalTime(c.Query("last_followup_to"))
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	result, err := h.service.List(c.Request.Context(), ListQuery{Keyword: c.Query("keyword"), CustomerType: c.Query("type"), Industry: c.Query("industry"), Region: c.Query("region"), OwnerID: c.Query("owner_id"), Status: c.Query("status"), QuickFilter: c.Query("quick_filter"), CreatedFrom: createdFrom, CreatedTo: createdTo, LastFollowupFrom: lastFollowupFrom, LastFollowupTo: lastFollowupTo, Page: page, PageSize: pageSize, SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order")})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func positiveQueryInt(value string, maximum int) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || (maximum > 0 && parsed > maximum) {
		return 0, ErrInvalidQuery
	}
	return parsed, nil
}

func validCustomerListKeys(c *gin.Context) bool {
	allowed := map[string]struct{}{
		"keyword": {}, "type": {}, "industry": {}, "region": {}, "owner_id": {}, "status": {},
		"quick_filter": {}, "created_from": {}, "created_to": {}, "last_followup_from": {}, "last_followup_to": {},
		"page": {}, "page_size": {}, "sort_by": {}, "sort_order": {},
	}
	for key := range c.Request.URL.Query() {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func parseOptionalTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func (h *Handler) ListContacts(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	result, err := h.service.ListContacts(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ListStakeholders(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	result, err := h.service.ListStakeholders(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ReplaceStakeholders(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	var input ReplaceStakeholdersRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.ReplaceStakeholders(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ListInformationSystems(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	result, err := h.service.ListInformationSystems(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ReplaceInformationSystems(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	var input ReplaceInformationSystemsRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.ReplaceInformationSystems(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ListChangeLogs(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	result, err := h.service.ListChangeLogs(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) OpportunityHistory(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	result, err := h.service.ListOpportunityHistory(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ProjectHistory(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	if !validProjectHistoryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	page, err := positiveQueryInt(c.DefaultQuery("page", "1"), maxProjectHistoryPage)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	pageSize, err := positiveQueryInt(c.DefaultQuery("page_size", "20"), 100)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	result, err := h.service.ProjectHistory(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func validProjectHistoryKeys(c *gin.Context) bool {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, entries := range values {
		if key != "page" && key != "page_size" || len(entries) != 1 {
			return false
		}
	}
	return true
}

func (h *Handler) CreateExport(c *gin.Context) {
	response.Error(c, h.service.CreateExport(c.Request.Context()))
}

func (h *Handler) PreviewImport(c *gin.Context) {
	// 预览阶段只接受一个 xlsx 文件和一条原因：先限制整个 multipart 请求，再分别限制文件
	// 与文本字段大小，并拒绝未知或重复字段，避免解析器在业务校验前消耗无界资源。
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		response.Error(c, invalidCustomerBody())
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, importMultipartBodyLimit)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	var file []byte
	var reason string
	seen := make(map[string]bool, 2)
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			response.Error(c, invalidCustomerBody())
			return
		}
		name := part.FormName()
		if (name != "file" && name != "reason") || seen[name] {
			_ = part.Close()
			response.Error(c, invalidCustomerBody())
			return
		}
		seen[name] = true
		if name == "file" {
			if part.FileName() == "" || !strings.EqualFold(filepathExtension(part.FileName()), ".xlsx") {
				_ = part.Close()
				response.Error(c, ErrImportInvalidFile)
				return
			}
			file, err = io.ReadAll(io.LimitReader(part, importMaxFileBytes+1))
		} else {
			var raw []byte
			raw, err = io.ReadAll(io.LimitReader(part, 2001))
			reason = string(raw)
			if len(raw) > 2000 {
				err = ErrImportInvalidFile
			}
		}
		_ = part.Close()
		if err != nil || len(file) > importMaxFileBytes {
			response.Error(c, ErrImportInvalidFile)
			return
		}
	}
	if !seen["file"] || !seen["reason"] || len(file) == 0 {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.PreviewImport(c.Request.Context(), file, reason)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

func (h *Handler) CommitImport(c *gin.Context) {
	// 提交阶段不再次上传文件，而是引用预览生成的 jobNo，并用幂等键约束重复确认；
	// 预览令牌、内容摘要和逐行写入规则由服务层复核，不能只信任前端预览结果。
	var input ImportCommitRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.CommitImport(c.Request.Context(), c.Param("jobNo"), c.GetHeader("Idempotency-Key"), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ImportErrors(c *gin.Context) {
	// 错误明细按当前主体和导入任务授权后生成；下载禁用缓存与 MIME 嗅探，文件名由服务端产生。
	contents, filename, err := h.service.ImportErrorsCSV(c.Request.Context(), c.Param("jobNo"))
	if err != nil {
		response.Error(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", contents)
}

func filepathExtension(name string) string {
	index := strings.LastIndex(name, ".")
	if index < 0 {
		return ""
	}
	return name[index:]
}

func customerID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return 0, false
	}
	return id, true
}

func (h *Handler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return
	}
	var input UpdateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.Update(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) Void(c *gin.Context)    { h.changeStatus(c, true) }
func (h *Handler) Restore(c *gin.Context) { h.changeStatus(c, false) }
func (h *Handler) changeStatus(c *gin.Context, void bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return
	}
	var input StatusChangeRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	var result *Response
	if void {
		result, err = h.service.Void(c.Request.Context(), id, input)
	} else {
		result, err = h.service.Restore(c.Request.Context(), id, input)
	}
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) CreateFollowup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return
	}
	var input FollowupCreateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidCustomerBody())
		return
	}
	result, err := h.service.CreateFollowup(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

func invalidCustomerBody() error {
	return apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request")
}

func (h *Handler) ListFollowups(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	result, err := h.service.ListFollowups(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}
