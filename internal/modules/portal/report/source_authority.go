package report

import (
	"context"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/sourceauthority"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"time"
)

// ClaimCallbackSource 将旧PMS回调限制在CRM权威并与回调事务共同提交。
// 参数：ctx为原回调事务，tenant为已认证租户。
// 返回值：本地来源冲突或数据库错误，不放宽既有回调校验。
func (r *GORMRepository) ClaimCallbackSource(ctx context.Context, tenant string) error {
	return sourceauthority.Claim(database.FromContext(ctx, r.db), tenant, "crm", "portal-report-callback", time.Now())
}
