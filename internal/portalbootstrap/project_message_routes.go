package portalbootstrap

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectmessage"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

func projectMessageCustomerActor(c *gin.Context) projectmessage.CustomerActor {
	session := currentSession(c)
	return projectmessage.CustomerActor{TenantID: session.TenantID, CustomerID: session.CustomerID, AccountID: session.PlatformUserID}
}

func createProjectConversation(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.ProjectMessages == nil || !onlyProjectQueryKeys(c) {
			response.Error(c, projectmessage.ErrValidation)
			return
		}
		value, err := deps.ProjectMessages.Create(c.Request.Context(), projectMessageCustomerActor(c), c.Param("projectID"), projectmessage.CreateCommand{IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.Created(c, publicConversation(value))
	}
}

func getCurrentProjectConversation(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		before, size, ok := bindProjectMessagePagination(c)
		if !ok {
			return
		}
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		value, err := deps.ProjectMessages.CurrentCustomer(c.Request.Context(), projectMessageCustomerActor(c), c.Param("projectID"), before, size)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicConversationDetail(value))
	}
}

func getProjectConversation(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		before, size, ok := bindProjectMessagePagination(c)
		if !ok {
			return
		}
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		value, err := deps.ProjectMessages.GetCustomer(c.Request.Context(), projectMessageCustomerActor(c), c.Param("id"), before, size)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicConversationDetail(value))
	}
}

func sendProjectMessage(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.ProjectMessages.SendCustomer(c.Request.Context(), projectMessageCustomerActor(c), c.Param("id"), projectmessage.SendCommand{Content: body.Content, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicConversationDetail(value))
	}
}

func readProjectMessages(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		var body struct {
			MessageCursors             []string `json:"message_cursors"`
			LegacyThroughMessageCursor string   `json:"through_message_cursor"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.ProjectMessages.ReadCustomer(c.Request.Context(), projectMessageCustomerActor(c), c.Param("id"), projectmessage.ReadCommand{MessageCursors: body.MessageCursors, LegacyThroughMessageCursor: body.LegacyThroughMessageCursor})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

// 专用项目系统机器客户端提供精确 Portal 收件账号；服务查询还要求其等于当前源系统项目快照，
// 单独伪造请求头不能获得访问权。
func projectMessageManagerActor(c *gin.Context) (projectmessage.ManagerActor, error) {
	principal, ok := sharedauth.FromContext(c.Request.Context())
	accountID := strings.TrimSpace(c.GetHeader("X-Manager-Portal-Account-ID"))
	if !ok || principal.TenantID == "" || accountID == "" {
		return projectmessage.ManagerActor{}, apperror.ErrUnauthenticated
	}
	return projectmessage.ManagerActor{TenantID: principal.TenantID, AccountID: accountID}, nil
}

func listProjectConversationsForManager(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, size, ok := bindProjectPagination(c)
		if !ok {
			return
		}
		actor, err := projectMessageManagerActor(c)
		if err != nil {
			response.Error(c, err)
			return
		}
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		value, err := deps.ProjectMessages.ListManager(c.Request.Context(), actor, page, size)
		if err != nil {
			response.Error(c, err)
			return
		}
		items := make([]gin.H, 0, len(value.Items))
		for i := range value.Items {
			items = append(items, publicConversation(&value.Items[i]))
		}
		response.OK(c, gin.H{"items": items, "page": value.Page, "page_size": value.PageSize, "total": value.Total})
	}
}

func getProjectConversationForManager(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		before, size, ok := bindProjectMessagePagination(c)
		if !ok {
			return
		}
		actor, err := projectMessageManagerActor(c)
		if err != nil {
			response.Error(c, err)
			return
		}
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		value, err := deps.ProjectMessages.GetManager(c.Request.Context(), actor, c.Param("id"), before, size)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicConversationDetail(value))
	}
}

func sendProjectMessageForManager(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor, err := projectMessageManagerActor(c)
		if err != nil {
			response.Error(c, err)
			return
		}
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.ProjectMessages.SendManager(c.Request.Context(), actor, c.Param("id"), projectmessage.SendCommand{Content: body.Content, IdempotencyKey: c.GetHeader("Idempotency-Key")})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, publicConversationDetail(value))
	}
}

func readProjectMessagesForManager(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor, err := projectMessageManagerActor(c)
		if err != nil {
			response.Error(c, err)
			return
		}
		if deps.ProjectMessages == nil {
			response.Error(c, projectmessage.ErrNotFound)
			return
		}
		var body struct {
			MessageCursors             []string `json:"message_cursors"`
			LegacyThroughMessageCursor string   `json:"through_message_cursor"`
		}
		if !decode(c, &body) {
			return
		}
		value, err := deps.ProjectMessages.ReadManager(c.Request.Context(), actor, c.Param("id"), projectmessage.ReadCommand{MessageCursors: body.MessageCursors, LegacyThroughMessageCursor: body.LegacyThroughMessageCursor})
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, value)
	}
}

func publicConversation(value *projectmessage.Conversation) gin.H {
	return gin.H{"id": value.PublicID, "project_id": value.ProjectID, "manager_name": value.ManagerNameSnapshot, "last_message_at": value.LastMessageAt, "created_at": value.CreatedAt, "version": value.Version}
}

func publicConversationDetail(value *projectmessage.Detail) gin.H {
	items := make([]gin.H, 0, len(value.Messages.Items))
	for i := range value.Messages.Items {
		item := &value.Messages.Items[i]
		items = append(items, gin.H{"cursor": item.Cursor, "sender_type": item.SenderType, "content": item.Content, "accepted_at": item.AcceptedAt})
	}
	return gin.H{"conversation": publicConversation(&value.Conversation), "messages": gin.H{"items": items, "page": 1, "page_size": value.Messages.PageSize, "total": value.Messages.Total, "has_more": value.Messages.HasMore, "next_before": value.Messages.NextBefore, "page_order": "LATEST_FIRST", "items_order": "OLDEST_FIRST"}, "read_state": value.ReadState}
}

// 兼容旧客户端的 page=1，同时历史翻页统一使用不透明、排他的 keyset 游标；拒绝 page>1，
// 因为新增消息期间 OFFSET 分页无法保持稳定。
func bindProjectMessagePagination(c *gin.Context) (string, int, bool) {
	page, size, ok := bindProjectPagination(c, "before")
	if !ok {
		return "", 0, false
	}
	if page != 1 {
		response.Error(c, apperror.New(400, "PORTAL_PROJECT_MESSAGE_OFFSET_PAGE_UNSUPPORTED", "use the opaque before cursor"))
		return "", 0, false
	}
	return strings.TrimSpace(c.Query("before")), size, true
}
