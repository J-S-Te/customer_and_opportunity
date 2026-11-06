package localidentity

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct{ service *Service }

// NewHandler 构造独立管理接口，鉴权后仍由每项服务精确校验超管权限。
// 参数：service为本地模式业务服务。
// 返回值：管理处理器；nil服务不产生成功占位。
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes 将本地管理接口挂到门户认证与CSRF保护组。
// 参数：group必须为组合根建立的管理路由组，不能复用客户业务免ACL路径。
// 返回值：无；服务未装配时仅返回明确不可用。
func (h *Handler) RegisterRoutes(group *gin.RouterGroup) {
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, private")
		c.Header("Pragma", "no-cache")
		if h == nil || h.service == nil {
			response.Error(c, ErrUnavailable)
			c.Abort()
		}
	})
	group.GET("/customers", h.listCustomers)
	group.POST("/customers", h.createCustomer)
	group.GET("/customers/:id/contacts", h.listContacts)
	group.POST("/contacts", h.createContact)
	group.GET("/customers/:id/accounts", h.listAccounts)
	group.GET("/customers/:id/project-access", h.listProjectAccess)
	group.GET("/customers/:id/invitations", h.listInvitations)
	group.POST("/invitations", h.createInvitation)
	group.POST("/invitations/:id/revoke", h.revokeInvitation)
	group.GET("/operations", h.listOperations)
	group.POST("/operations/:number/retry", h.retryOperation)
	group.POST("/access/disable", h.disableAccess)
	group.GET("/customers/:id/projects", h.listProjects)
	group.POST("/projects", h.saveProject)
	group.PUT("/project-access", h.projectAccess)
	group.POST("/reports", h.createReport)
	group.POST("/operations/:number/report-file", h.retryReportFile)
}

// localID 解析规范十进制真实资源坐标。
// 参数：value为路由或查询参数，zero允许零游标。
// 返回值：资源编号及是否有效。
func localID(value string, zero bool) (uint64, bool) {
	n, e := strconv.ParseUint(value, 10, 64)
	return n, e == nil && (zero || n > 0) && strconv.FormatUint(n, 10) == value
}

// decode 管理请求严格拒绝未知字段和超限JSON。
// 参数：c为HTTP请求，body为明确DTO。
// 返回值：解析是否成功，失败已输出稳定错误。
func decode(c *gin.Context, body any) bool {
	if requestbody.DecodeJSONWithLimit(c, body, 16<<10) != nil {
		response.Error(c, ErrInvalid)
		return false
	}
	return true
}

// reply 返回真实结果或安全错误，不将带操作号的失败转为成功。
// 参数：c为HTTP请求，value为结果，err为业务错误。
// 返回值：无。
func reply(c *gin.Context, value any, err error) {
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

// replyOperation 在失败回执中保留可恢复的原操作号和真实阶段。
// 参数：c为请求，value为真实持久操作，err为未完成原因。
// 返回值：无；失败保持非2xx。
func replyOperation(c *gin.Context, value OperationResult, err error) {
	if err != nil {
		app := *apperror.As(err)
		if value.Operation.OperationNo != "" {
			app.Details = map[string]any{"operation": value.Operation}
		}
		response.Error(c, &app)
		return
	}
	response.OK(c, value)
}

// createCustomer 登记当前租户本地客户。
// 参数：c为已认证管理请求。
// 返回值：无；输出真实业务回执。
func (h *Handler) createCustomer(c *gin.Context) {
	var b CustomerInput
	if !decode(c, &b) {
		return
	}
	v, e := h.service.CreateCustomer(c.Request.Context(), b)
	reply(c, v, e)
}

// listCustomers 按键集游标列出有界目录。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) listCustomers(c *gin.Context) {
	after := uint64(0)
	if v := c.Query("after"); v != "" {
		var ok bool
		after, ok = localID(v, true)
		if !ok {
			response.Error(c, ErrInvalid)
			return
		}
	}
	v, e := h.service.ListCustomers(c.Request.Context(), after, 100)
	reply(c, v, e)
}

// createContact 创建加密登记联系人。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) createContact(c *gin.Context) {
	var b ContactInput
	if !decode(c, &b) {
		return
	}
	v, e := h.service.CreateContact(c.Request.Context(), b)
	reply(c, v, e)
}

// customerParam 从管理路由读取规范客户坐标。
// 参数：c为当前管理请求。
// 返回值：真实客户编号；无效时已写错误。
func customerParam(c *gin.Context) (uint64, bool) {
	id, ok := localID(c.Param("id"), false)
	if !ok {
		response.Error(c, ErrInvalid)
	}
	return id, ok
}

// listContacts 查询客户登记联系人。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) listContacts(c *gin.Context) {
	id, ok := customerParam(c)
	if !ok {
		return
	}
	v, e := h.service.ListContacts(c.Request.Context(), id)
	reply(c, v, e)
}

// listAccounts 查询客户真实账号映射元数据。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) listAccounts(c *gin.Context) {
	id, ok := customerParam(c)
	if !ok {
		return
	}
	v, e := h.service.ListAccounts(c.Request.Context(), id)
	reply(c, v, e)
}

// listInvitations 查询客户真实本地邀请。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) listInvitations(c *gin.Context) {
	id, ok := customerParam(c)
	if !ok {
		return
	}
	v, e := h.service.ListInvitations(c.Request.Context(), id)
	reply(c, v, e)
}

// createInvitation 以原幂等键开户并创建本地邀请。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) createInvitation(c *gin.Context) {
	var b InvitationInput
	if !decode(c, &b) {
		return
	}
	b.IdempotencyKey = c.GetHeader("Idempotency-Key")
	v, e := h.service.CreateInvitation(c.Request.Context(), b)
	replyOperation(c, v, e)
}

// revokeInvitation 依据并发版本撤销待使用邀请。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) revokeInvitation(c *gin.Context) {
	id, ok := customerParam(c)
	if !ok {
		return
	}
	var b struct {
		ExpectedVersion uint64 `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if !decode(c, &b) {
		return
	}
	e := h.service.RevokeInvitation(c.Request.Context(), id, b.ExpectedVersion, b.Reason)
	reply(c, map[string]any{"revoked": true}, e)
}

// listOperations 查询保留失败状态的操作账本。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) listOperations(c *gin.Context) {
	customer := uint64(0)
	if raw := c.Query("customer_id"); raw != "" {
		var ok bool
		customer, ok = localID(raw, false)
		if !ok {
			response.Error(c, ErrInvalid)
			return
		}
	}
	v, e := h.service.ListOperations(c.Request.Context(), customer)
	reply(c, v, e)
}

// retryOperation 只根据原操作号精确续跑。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) retryOperation(c *gin.Context) {
	var b struct{}
	if !decode(c, &b) {
		return
	}
	v, e := h.service.ResumeOperation(c.Request.Context(), c.Param("number"))
	replyOperation(c, v, e)
}

// disableAccess 冻结本地访问并恢复真实平台撤权。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) disableAccess(c *gin.Context) {
	var b DisableInput
	if !decode(c, &b) {
		return
	}
	b.IdempotencyKey = c.GetHeader("Idempotency-Key")
	v, e := h.service.DisableAccess(c.Request.Context(), b)
	replyOperation(c, v, e)
}

// saveProject 登记或版本化更新本地项目。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) saveProject(c *gin.Context) {
	var b ProjectInput
	if !decode(c, &b) {
		return
	}
	v, e := h.service.SaveProject(c.Request.Context(), b)
	reply(c, v, e)
}

// listProjects 查询本地客户项目目录。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) listProjects(c *gin.Context) {
	id, ok := customerParam(c)
	if !ok {
		return
	}
	after := uint64(0)
	if raw := c.Query("after"); raw != "" {
		var valid bool
		after, valid = localID(raw, true)
		if !valid {
			response.Error(c, ErrInvalid)
			return
		}
	}
	v, e := h.service.ListProjects(c.Request.Context(), id, after)
	reply(c, v, e)
}

// projectAccess 维护精确账号的MANUAL授权。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) projectAccess(c *gin.Context) {
	var b ProjectAccess
	if !decode(c, &b) {
		return
	}
	v, e := h.service.SetProjectAccess(c.Request.Context(), b)
	reply(c, ProjectAccessSummary{ProjectID: v.ProjectID, AccountID: v.AccountID, Version: v.Version, Enabled: v.Status == "ACTIVE"}, e)
}

// reportMultipart 限制文件请求为单PDF和一份严格元数据。
// 参数：c为HTTP请求，metadata允许创建时的JSON表单。
// 返回值：原文件流、文件名与是否成功；调用者负责关闭流。
func reportMultipart(c *gin.Context, metadata any) (io.ReadCloser, string, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, (20<<20)+(64<<10))
	if c.Request.ParseMultipartForm(1<<20) != nil {
		response.Error(c, ErrInvalid)
		return nil, "", false
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if (metadata != nil && (len(c.Request.MultipartForm.Value) != 1 || len(c.Request.MultipartForm.Value["metadata"]) != 1)) || (metadata == nil && len(c.Request.MultipartForm.Value) != 0) {
		response.Error(c, ErrInvalid)
		return nil, "", false
	}
	if len(c.Request.MultipartForm.File) != 1 || len(c.Request.MultipartForm.File["file"]) != 1 {
		response.Error(c, ErrInvalid)
		return nil, "", false
	}
	file, header, e := c.Request.FormFile("file")
	if e != nil || header.Size <= 0 || header.Size > 20<<20 {
		if file != nil {
			file.Close()
		}
		response.Error(c, ErrInvalid)
		return nil, "", false
	}
	if metadata != nil {
		if !decodeMetadata(c.Request.FormValue("metadata"), metadata) {
			file.Close()
			response.Error(c, ErrInvalid)
			return nil, "", false
		}
	}
	return file, header.Filename, true
}

// createReport 创建待安全摄取的本地报告来源。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) createReport(c *gin.Context) {
	if _, err := h.service.principal(c.Request.Context(), PermissionReportManage, true); err != nil {
		response.Error(c, err)
		return
	}
	var b ReportInput
	file, name, ok := reportMultipart(c, &b)
	if !ok {
		return
	}
	defer file.Close()
	b.FileName = name
	b.IdempotencyKey = c.GetHeader("Idempotency-Key")
	v, e := h.service.CreateReport(c.Request.Context(), b, file)
	replyOperation(c, v, e)
}

// retryReportFile 仅向原操作重传同一PDF。
// 参数：c为已认证管理请求。
// 返回值：无。
func (h *Handler) retryReportFile(c *gin.Context) {
	if _, err := h.service.principal(c.Request.Context(), PermissionReportManage, true); err != nil {
		response.Error(c, err)
		return
	}
	file, name, ok := reportMultipart(c, nil)
	if !ok {
		return
	}
	defer file.Close()
	v, e := h.service.RetryReportUpload(c.Request.Context(), c.Param("number"), name, file)
	replyOperation(c, v, e)
}

// listProjectAccess 返回管理授权版本以支持精确撤销和乐观更新。
// 参数：c为已认证项目管理请求。
// 返回值：无。
func (h *Handler) listProjectAccess(c *gin.Context) {
	id, ok := customerParam(c)
	if !ok {
		return
	}
	v, e := h.service.ListProjectAccess(c.Request.Context(), id)
	reply(c, v, e)
}
