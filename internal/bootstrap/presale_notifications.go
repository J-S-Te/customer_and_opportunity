package bootstrap

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"gorm.io/gorm"
)

type presaleNotificationWriter struct{ db *gorm.DB }

func (w presaleNotificationWriter) Write(ctx context.Context, n presale.WorkflowNotification) error {
	now := time.Now().UTC()
	return w.db.WithContext(ctx).Exec(`INSERT INTO crm_notifications (source_event_id,type,opportunity_id,opportunity_version,opportunity_no,opportunity_name,request_id,request_no,assignment_id,progress_id,recipient_id,recipient_kind,title,body,target_path,status,created_at,updated_at,version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id`, n.TenantID+":"+n.Type+":"+n.RequestNo, n.Type, 0, 0, "", "", n.RequestID, n.RequestNo, 0, 0, n.RecipientID, "USER", n.Title, n.Body, "/customer-opportunity/presale?request_id="+n.RequestNo, "UNREAD", now, now, 1).Error
}
