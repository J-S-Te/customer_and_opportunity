package presale

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

// GORMWorkflowNotificationWriter 把售前请求级站内消息写入 crm_notifications。
// 它只代表本地站内消息已可见，不代表邮件、短信或 IM 等外部渠道已投递。
type GORMWorkflowNotificationWriter struct{ DB *gorm.DB }

func NewGORMWorkflowNotificationWriter(db *gorm.DB) *GORMWorkflowNotificationWriter {
	return &GORMWorkflowNotificationWriter{DB: db}
}

func (w *GORMWorkflowNotificationWriter) Write(ctx context.Context, n WorkflowNotification) error {
	if w == nil || w.DB == nil {
		return ErrDependencyUnavailable
	}
	now := time.Now().UTC()
	sourceEventID := WorkflowNotificationSourceEventID(n)
	return database.FromContext(ctx, w.DB).Exec(`INSERT INTO crm_notifications (tenant_id,created_by,updated_by,source_event_id,type,opportunity_id,opportunity_version,opportunity_no,opportunity_name,request_id,request_no,assignment_id,progress_id,recipient_id,recipient_kind,title,body,target_path,status,created_at,updated_at,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`,
		n.TenantID, "system", "system", sourceEventID, n.Type, 0, 0, "", "", n.RequestID, n.RequestNo, n.AssignmentID, 0,
		n.RecipientID, "USER", n.Title, n.Body, "/customer-opportunity/presale?request_id="+strconv.FormatUint(n.RequestID, 10),
		"UNREAD", now, now, 1).Error
}

// WorkflowNotificationSourceEventID 绑定租户、类型、申请、收件人、指派和审批节点。
// 基础平台要求事件编码以字母开头；租户 ULID 以数字开头，不能直接作为前缀。
func WorkflowNotificationSourceEventID(n WorkflowNotification) string {
	raw := fmt.Sprintf("%s:%s:%s:%s:%d:%d", n.TenantID, n.Type, n.RequestNo, n.RecipientID, n.AssignmentID, n.ApprovalNode)
	// 保留无判别字段时的历史算法，滚动升级期间同一审批事件仍可幂等去重。
	if n.SourceDiscriminator != "" {
		raw += ":" + n.SourceDiscriminator
	}
	return fmt.Sprintf("CRM_%X", sha256.Sum256([]byte(raw)))
}
