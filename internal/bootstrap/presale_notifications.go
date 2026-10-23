package bootstrap

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"gorm.io/gorm"
)

// presaleNotificationWriter 保留 bootstrap 原有的注入点，具体写入逻辑由 presale
// 包统一实现，供 API 进程和独立人员同步 Worker 复用同一事件身份算法。
type presaleNotificationWriter struct{ db *gorm.DB }

func (w presaleNotificationWriter) Write(ctx context.Context, n presale.WorkflowNotification) error {
	return presale.NewGORMWorkflowNotificationWriter(w.db).Write(ctx, n)
}

func presaleNotificationSourceEventID(n presale.WorkflowNotification) string {
	return presale.WorkflowNotificationSourceEventID(n)
}
