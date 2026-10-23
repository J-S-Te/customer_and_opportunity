package presale

import (
	"context"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

// FreezeChecker 报告用户当前是否因交接被冻结。
type FreezeChecker interface {
	ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error)
}

var ErrUserFrozenByHandover = apperror.New(409, "CRM_PRESALE_USER_FROZEN", "用户处于人员交接冻结中，暂不能接收新的售前责任")

func (s *Service) UseFreezeChecker(checker FreezeChecker) *Service {
	s.freeze = checker
	return s
}

func (s *Service) ensureUserNotFrozen(ctx context.Context, tenantID, userID string) error {
	if s == nil || s.freeze == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	frozen, err := s.freeze.ActiveFreezeUser(ctx, tenantID, strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	if frozen {
		return ErrUserFrozenByHandover
	}
	return nil
}
