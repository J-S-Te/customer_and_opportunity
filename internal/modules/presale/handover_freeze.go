package presale

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/handoverguard"
)

// FreezeChecker 报告用户当前是否因交接被冻结。
type FreezeChecker interface {
	ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error)
}

var ErrUserFrozenByHandover = apperror.New(409, "CRM_PRESALE_USER_FROZEN", "用户处于人员交接冻结中，暂不能接收新的售前责任")

// ErrFreezeSubjectRequired 表示写路径没有给出承接人，无法判断是否处于交接冻结。
var ErrFreezeSubjectRequired = apperror.New(http.StatusUnprocessableEntity, "CRM_PRESALE_ASSIGNEE_REQUIRED", "承接人不能为空，无法校验人员交接冻结")

// ErrFreezeCheckerUnavailable 表示交接冻结强制未生效（检查器未接线，
// 或生产写事务里拿不到门闩句柄）。此时必须拒绝写入，
// 不能退回"未接线就放行"的 fail-open 行为。
var ErrFreezeCheckerUnavailable = apperror.New(http.StatusInternalServerError, "CRM_PRESALE_HANDOVER_FREEZE_UNAVAILABLE", "人员交接冻结检查未生效，拒绝写入售前责任")

func (s *Service) UseFreezeChecker(checker FreezeChecker) *Service {
	s.freeze = checker
	return s
}

// HandoverFreezeEnforced 供组合根装配断言：写路径是否已接入交接冻结强制。
func (s *Service) HandoverFreezeEnforced() bool { return s != nil && s.freeze != nil }

// ensureUserNotFrozen 在写路径上阻止把新责任分配给交接中的用户。
// 检查器未接线或承接人为空时一律拒绝（fail-closed）；查询失败同样拒绝。
func (s *Service) ensureUserNotFrozen(ctx context.Context, tenantID, userID string) error {
	if s == nil || s.freeze == nil {
		return ErrFreezeCheckerUnavailable
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrFreezeSubjectRequired
	}
	frozen, err := s.freeze.ActiveFreezeUser(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	if frozen {
		return ErrUserFrozenByHandover
	}
	return nil
}

// guardUserWrite 是写事务内的最终防线：先按检查器复核冻结，
// 再对 (tenant, user) 门闩行加行锁，与交接侧 CreateFreeze 的同一行锁串行化。
// 必须由写事务的派生上下文调用；责任写入路径全部在 repo.WithTransaction 内完成。
func (s *Service) guardUserWrite(ctx context.Context, tenantID, userID string) error {
	if err := s.ensureUserNotFrozen(ctx, tenantID, userID); err != nil {
		return err
	}
	tx := database.FromContext(ctx, nil)
	if tx == nil {
		// 单元测试夹具使用内存仓储、没有数据库事务，由上面的检查器复核承担同一 fail-closed 语义；
		// 生产仓储恒有数据库，缺失事务属于装配错误，必须拒绝写入。
		if s.db == nil {
			return nil
		}
		return ErrFreezeCheckerUnavailable
	}
	if err := handoverguard.Guard(tx, tenantID, userID); err != nil {
		if errors.Is(err, handoverguard.ErrUserFrozen) {
			return ErrUserFrozenByHandover
		}
		return err
	}
	return nil
}
