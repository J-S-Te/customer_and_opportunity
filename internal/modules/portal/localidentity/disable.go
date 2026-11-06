package localidentity

import (
	"context"
	"errors"
	"fmt"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DisableInput struct {
	CustomerID      uint64 `json:"customer_id"`
	PlatformUserID  string `json:"platform_user_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason"`
	IdempotencyKey  string `json:"-"`
}

// DisableAccess 先冻结精确本地映射与旧会话，再持久化恢复真实平台撤权。
// 参数：ctx 为真实管理员，input 为客户、主体、版本与停用原因。
// 返回值：真实操作状态；平台失败仍保持本地拒绝，按原操作号恢复。
func (s *Service) DisableAccess(ctx context.Context, input DisableInput) (OperationResult, error) {
	p, err := s.principal(ctx, PermissionAccessDisable, true)
	if err != nil {
		return OperationResult{}, err
	}
	if s.Accounts == nil || input.CustomerID == 0 || input.ExpectedVersion == 0 || !validText(input.PlatformUserID, 128, true) || !validText(input.Reason, 500, true) || !validText(input.IdempotencyKey, 128, true) {
		return OperationResult{}, ErrInvalid
	}
	var op Operation
	err = s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.Repo.lockCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND created_by=? AND idempotency_key=?", p.TenantID, p.UserID, input.IdempotencyKey).Take(&op).Error
		if err == nil {
			if op.RequestHash != requestHash(input) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if _, err := s.Repo.lockCustomer(tx, p.TenantID, input.CustomerID); err != nil {
			return err
		}
		var link account.IdentityLink
		if err := s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND version=?", p.TenantID, input.CustomerID, input.PlatformUserID, input.ExpectedVersion).Take(&link).Error; err != nil {
			return ErrConflict
		}
		number, err := s.token(16)
		if err != nil {
			return err
		}
		op = Operation{Record: s.record(p), OperationNo: number, IdempotencyKey: input.IdempotencyKey, RequestHash: requestHash(input), Kind: "DISABLE", CustomerID: input.CustomerID, PlatformUserID: input.PlatformUserID, Stage: "LOCAL_DISABLED", Status: "PROCESSING", SnapshotCipher: []byte{}}
		if _, err := s.Accounts.Disable(tx, account.DisableCommand{TenantID: p.TenantID, CustomerID: input.CustomerID, PlatformUserID: input.PlatformUserID, ActorID: p.UserID, Reason: input.Reason, IdempotencyKey: "portal-local-v1:" + number}); err != nil {
			return err
		}
		if err := s.Repo.tx(tx).Model(&Invitation{}).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND status='PENDING'", p.TenantID, input.CustomerID, input.PlatformUserID).Updates(map[string]any{"status": "REVOKED", "revoked_reason": input.Reason, "updated_at": s.now()}).Error; err != nil {
			return err
		}
		if err := s.Repo.tx(tx).Create(&op).Error; err != nil {
			return err
		}
		return s.audit(tx, p, "PORTAL_LOCAL_ACCESS_FROZEN", "portal_local_operation", number)
	})
	if err != nil {
		return OperationResult{}, err
	}
	return s.ResumeOperation(ctx, op.OperationNo)
}

// resumeDisable 只在本地已冻结后按真实回执撤销平台角色及授权绑定。
// 参数：ctx/p 为管理请求，op 为原操作账本。
// 返回值：平台回执或持久化失败，不恢复本地访问。
func (s *Service) resumeDisable(ctx context.Context, p auth.Principal, op *Operation) error {
	if op.Stage == "LOCAL_DISABLED" {
		if err := s.Platform.RevokeRole(ctx, op.PlatformUserID, op.OperationNo); err != nil {
			return err
		}
		if err := s.advanceOperation(ctx, p, op, "ROLE_REVOKED", map[string]any{}); err != nil {
			return err
		}
	}
	if op.Stage != "ROLE_REVOKED" {
		return ErrConflict
	}
	if err := s.Platform.DisableBinding(ctx, op.PlatformUserID, op.CustomerID, op.OperationNo); err != nil {
		return err
	}
	return s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		if err := s.advanceOperation(tx, p, op, "DISABLED", map[string]any{"status": "COMPLETED", "lease_owner": "", "lease_until": nil, "last_error": ""}); err != nil {
			return err
		}
		op.Status = "COMPLETED"
		return s.audit(tx, p, "PORTAL_LOCAL_ACCESS_DISABLED", "portal_local_operation", fmt.Sprint(op.ID))
	})
}
