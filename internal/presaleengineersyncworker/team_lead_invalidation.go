package presaleengineersyncworker

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"gorm.io/gorm"
)

const teamLeadInvalidationReason = "PMS 人员同步标记团队负责人离职或停用"

// invalidateTeamLeadsForInactiveEngineers 处理团队负责人失效的待执行指派与执行中申请。
// PENDING_EXECUTION_ASSIGNMENT 退回待重选；EXECUTING 保持执行中，只通知技术总监人工接管。
// 查询以团队负责人本人对应的 PMS 缓存行 valid_flag=0 为准，不根据姓名或旧执行人记录猜测。
func invalidateTeamLeadsForInactiveEngineers(ctx context.Context, tx *gorm.DB, tenant string) error {
	if tx == nil || tenant == "" {
		return nil
	}
	requestIDs := make([]uint64, 0)
	err := tx.Raw(`SELECT DISTINCT r.id
		FROM crm_presale_requests r
		JOIN crm_presale_engineers e
		  ON e.tenant_id=r.tenant_id AND e.person_id=r.team_lead_person_id AND e.deleted_at IS NULL
		WHERE r.tenant_id=? AND r.deleted_at IS NULL AND r.team_lead_person_id<>''
		  AND r.status IN (?,?) AND e.valid_flag=0
		ORDER BY r.id`, tenant, string(presale.StatusPendingExecutionAssignment), string(presale.StatusExecuting)).Scan(&requestIDs).Error
	if err != nil {
		return err
	}
	if len(requestIDs) == 0 {
		return nil
	}
	service := presale.NewService(presale.NewGORMRepository(tx), nil, nil, presale.SystemClock{}, nil).
		UseWorkflowNotifications(presale.NewGORMWorkflowNotificationWriter(tx))
	for _, requestID := range requestIDs {
		if err = service.InvalidateTeamLead(ctx, tenant, requestID, teamLeadInvalidationReason); err != nil {
			return err
		}
	}
	return nil
}
