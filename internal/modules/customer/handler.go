package customer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

const (
	importMultipartBodyLimit = (10 << 20) + (64 << 10)
	customerExportBodyLimit  = 1 << 20
)

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
	creditLevel := strings.ToUpper(strings.TrimSpace(c.Query("credit_level")))
	if creditLevel != "" && creditLevel != "A" && creditLevel != "B" && creditLevel != "C" && creditLevel != "D" {
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
	result, err := h.service.List(c.Request.Context(), ListQuery{Keyword: c.Query("keyword"), CustomerType: c.Query("type"), Industry: c.Query("industry"), Region: c.Query("region"), OwnerID: c.Query("owner_id"), Status: c.Query("status"), CreditLevel: creditLevel, QuickFilter: c.Query("quick_filter"), CreatedFrom: createdFrom, CreatedTo: createdTo, LastFollowupFrom: lastFollowupFrom, LastFollowupTo: lastFollowupTo, Page: page, PageSize: pageSize, SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order")})
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
		"keyword": {}, "type": {}, "industry": {}, "region": {}, "owner_id": {}, "status": {}, "credit_level": {},
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
	query, err := customerExportListQuery(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	file, err := h.service.CreateExport(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	name := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(name)
	}()
	c.Header("Cache-Control", "no-store, private")
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="customers.csv"`)
	c.File(name)
}

// customerExportFilters 是导出端点接受的筛选负载，键与客户列表查询参数一一对应。
// page/page_size 仍被接受但不参与导出：导出固定 100 条/页，由服务端自行翻页。
type customerExportFilters struct {
	Keyword          string          `json:"keyword"`
	CustomerType     string          `json:"type"`
	Industry         string          `json:"industry"`
	Region           string          `json:"region"`
	OwnerID          string          `json:"owner_id"`
	Status           string          `json:"status"`
	CreditLevel      string          `json:"credit_level"`
	QuickFilter      string          `json:"quick_filter"`
	CreatedFrom      string          `json:"created_from"`
	CreatedTo        string          `json:"created_to"`
	LastFollowupFrom string          `json:"last_followup_from"`
	LastFollowupTo   string          `json:"last_followup_to"`
	SortBy           string          `json:"sort_by"`
	SortOrder        string          `json:"sort_order"`
	Page             json.RawMessage `json:"page"`
	PageSize         json.RawMessage `json:"page_size"`
}

type customerExportRequest struct {
	Filters *customerExportFilters `json:"filters"`
}

// customerExportListQuery 把可选的 {"filters":{...}} 请求体映射成列表查询。
// 空体保留旧调用兼容性（等价于不带筛选的导出）；一旦提供请求体则沿用严格 JSON 口径：
// 1 MiB 上限、只允许一个 JSON 对象、未知字段与尾随内容一律拒绝。日期必须为 RFC3339，
// 非法日期返回 422 而不是静默忽略，避免用户以为按条件导出、实际却拿到全量数据。
func customerExportListQuery(c *gin.Context) (ListQuery, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return ListQuery{}, nil
	}
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, customerExportBodyLimit)
	raw, err := io.ReadAll(reader)
	if err != nil {
		return ListQuery{}, invalidCustomerBody()
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ListQuery{}, nil
	}
	if trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return ListQuery{}, invalidCustomerBody()
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var payload customerExportRequest
	if err = decoder.Decode(&payload); err != nil {
		return ListQuery{}, invalidCustomerBody()
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ListQuery{}, invalidCustomerBody()
	}
	// {"filters":null} 与空体等价，继续按旧调用处理。
	if payload.Filters == nil {
		return ListQuery{}, nil
	}
	filters := payload.Filters
	query := ListQuery{
		Keyword: filters.Keyword, CustomerType: filters.CustomerType, Industry: filters.Industry,
		Region: filters.Region, OwnerID: filters.OwnerID, Status: filters.Status,
		CreditLevel: filters.CreditLevel, QuickFilter: filters.QuickFilter,
		SortBy: filters.SortBy, SortOrder: filters.SortOrder,
	}
	if query.CreatedFrom, err = parseOptionalTime(filters.CreatedFrom); err != nil {
		return ListQuery{}, ErrInvalidExportFilters
	}
	if query.CreatedTo, err = parseOptionalTime(filters.CreatedTo); err != nil {
		return ListQuery{}, ErrInvalidExportFilters
	}
	if query.LastFollowupFrom, err = parseOptionalTime(filters.LastFollowupFrom); err != nil {
		return ListQuery{}, ErrInvalidExportFilters
	}
	if query.LastFollowupTo, err = parseOptionalTime(filters.LastFollowupTo); err != nil {
		return ListQuery{}, ErrInvalidExportFilters
	}
	return query, nil
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

func (h *Handler) DownloadImportTemplate(c *gin.Context) {
	contents, err := customerImportTemplateWorkbook()
	if err != nil {
		response.Error(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", `attachment; filename="customer-import-template.xlsx"`)
	c.Data(http.StatusOK, customerImportTemplateContentType, contents)
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
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
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
