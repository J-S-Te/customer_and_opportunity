package opportunity

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/handoverguard"
)

// FreezeChecker 报告某用户当前是否处于人员交接冻结。
// 由组合根接线到交接仓储，避免领域模块直接依赖交接实现。
type FreezeChecker interface {
	ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error)
}

// ErrOwnerFrozenByHandover 表示目标负责人正处于离职交接冻结中，不得接收新责任。
var ErrOwnerFrozenByHandover = apperror.New(409, "CRM_OPPORTUNITY_OWNER_FROZEN", "负责人处于人员交接冻结中，暂不能接收新的商机责任")

// ErrFreezeCheckerUnavailable 表示交接冻结强制未生效（检查器未接线，
// 或生产写事务里拿不到门闩句柄）。此时必须拒绝写入，
// 不能退回"未接线就放行"的 fail-open 行为。
var ErrFreezeCheckerUnavailable = apperror.New(http.StatusInternalServerError, "CRM_OPPORTUNITY_HANDOVER_FREEZE_UNAVAILABLE", "人员交接冻结检查未生效，拒绝写入商机责任")

// UseFreezeChecker 注入冻结检查器（组合根调用）。
func (s *Service) UseFreezeChecker(checker FreezeChecker) *Service {
	s.freeze = checker
	return s
}

// HandoverFreezeEnforced 供组合根装配断言：写路径是否已接入交接冻结强制。
func (s *Service) HandoverFreezeEnforced() bool { return s != nil && s.freeze != nil }

// ensureOwnerNotFrozen 在写路径上阻止把新责任分配给交接中的用户。
// 检查器未接线或负责人为空时一律拒绝（fail-closed）；查询失败同样拒绝。
func (s *Service) ensureOwnerNotFrozen(ctx context.Context, tenantID, ownerUserID string) error {
	if s == nil || s.freeze == nil {
		return ErrFreezeCheckerUnavailable
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return ErrOwnerRequired
	}
	frozen, err := s.freeze.ActiveFreezeUser(ctx, tenantID, ownerUserID)
	if err != nil {
		return err
	}
	if frozen {
		return ErrOwnerFrozenByHandover
	}
	return nil
}

// guardOwnerWrite 是写事务内的最终防线：先按检查器复核冻结，
// 再对 (tenant, user) 门闩行加行锁，与交接侧 CreateFreeze 的同一行锁串行化。
// 必须由写事务的派生上下文调用（商机负责人/团队成员的写入都在事务内完成）。
func (s *Service) guardOwnerWrite(ctx context.Context, tenantID, ownerUserID string) error {
	if err := s.ensureOwnerNotFrozen(ctx, tenantID, ownerUserID); err != nil {
		return err
	}
	tx := database.FromContext(ctx, nil)
	if tx == nil {
		// 单元测试夹具没有数据库句柄，由上面的检查器复核承担同一 fail-closed 语义；
		// 生产装配恒有数据库，缺失事务属于装配错误，必须拒绝写入。
		if s.db == nil {
			return nil
		}
		return ErrFreezeCheckerUnavailable
	}
	if err := handoverguard.Guard(tx, tenantID, ownerUserID); err != nil {
		if errors.Is(err, handoverguard.ErrUserFrozen) {
			return ErrOwnerFrozenByHandover
		}
		return err
	}
	return nil
}
