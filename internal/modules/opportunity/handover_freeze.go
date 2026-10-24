package opportunity

import (
	"context"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

// FreezeChecker 报告某用户当前是否处于人员交接冻结。
// 由组合根接线到交接仓储，避免领域模块直接依赖交接实现。
type FreezeChecker interface {
	ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error)
}

// ErrOwnerFrozenByHandover 表示目标负责人正处于离职交接冻结中，不得接收新责任。
var ErrOwnerFrozenByHandover = apperror.New(409, "CRM_OPPORTUNITY_OWNER_FROZEN", "负责人处于人员交接冻结中，暂不能接收新的商机责任")

// UseFreezeChecker 注入冻结检查器（组合根调用）。
func (s *Service) UseFreezeChecker(checker FreezeChecker) *Service {
	s.freeze = checker
	return s
}

// ensureOwnerNotFrozen 在写路径上阻止把新责任分配给交接中的用户；
// 未接线检查器时保持既有行为，查询失败则 失败时安全拒绝 上报。
func (s *Service) ensureOwnerNotFrozen(ctx context.Context, tenantID, ownerUserID string) error {
	if s == nil || s.freeze == nil || strings.TrimSpace(ownerUserID) == "" {
		return nil
	}
	frozen, err := s.freeze.ActiveFreezeUser(ctx, tenantID, strings.TrimSpace(ownerUserID))
	if err != nil {
		return err
	}
	if frozen {
		return ErrOwnerFrozenByHandover
	}
	return nil
}
