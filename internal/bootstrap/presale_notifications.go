package bootstrap

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

type presaleNotificationWriter struct{ db *gorm.DB }

func (w presaleNotificationWriter) Write(ctx context.Context, n presale.WorkflowNotification) error {
	now := time.Now().UTC()
	// source_event_id 绑定到具体收件人，使同一请求的多级审批（启动通知第一级、
	// 流转通知下一级）互不冲突，仍可按 (tenant, source_event_id) 幂等去重。
	// 基础平台要求事件编码以字母开头；租户 ULID 以数字开头，不能直接作为前缀。
	sourceEventID := presaleNotificationSourceEventID(n)
	return database.FromContext(ctx, w.db).Exec(`INSERT INTO crm_notifications (tenant_id,created_by,updated_by,source_event_id,type,opportunity_id,opportunity_version,opportunity_no,opportunity_name,request_id,request_no,assignment_id,progress_id,recipient_id,recipient_kind,title,body,target_path,status,created_at,updated_at,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`, n.TenantID, "system", "system", sourceEventID, n.Type, 0, 0, "", "", n.RequestID, n.RequestNo, n.AssignmentID, 0, n.RecipientID, "USER", n.Title, n.Body, "/customer-opportunity/presale?request_id="+strconv.FormatUint(n.RequestID, 10), "UNREAD", now, now, 1).Error
}

func presaleNotificationSourceEventID(n presale.WorkflowNotification) string {
	raw := fmt.Sprintf("%s:%s:%s:%s:%d", n.TenantID, n.Type, n.RequestNo, n.RecipientID, n.AssignmentID)
	return fmt.Sprintf("CRM_%X", sha256.Sum256([]byte(raw)))
}
