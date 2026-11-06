package localidentity

import (
	"context"
	"errors"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// withInvitationTransaction 按客户、操作、主体的固定锁序保护邀请副作用与阶段推进。
// 参数：ctx/p为真实管理请求，op为持租约原操作，action为同一事务内的受控动作。
// 返回值：动作结果；客户停用、联系人失效、租约或阶段变化时拒绝。
func (s *Service) withInvitationTransaction(ctx context.Context, p auth.Principal, op *Operation, action func(context.Context) error) error {
	return s.Repo.WithTransaction(ctx, func(tx context.Context) error {
		customer, err := s.Repo.lockCustomer(tx, p.TenantID, op.CustomerID)
		if err != nil {
			return err
		}
		if customer.Status != "ACTIVE" {
			return ErrConflict
		}
		var current Operation
		err = s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id=? AND id=? AND lease_owner=? AND stage=? AND lease_until>?",
				p.TenantID, op.ID, op.LeaseOwner, op.Stage, s.now()).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if current.Kind != "INVITE" || current.CustomerID != op.CustomerID || current.ContactID != op.ContactID {
			return ErrConflict
		}
		if current.Stage != "PREPARED" &&
			(current.PlatformUserID != op.PlatformUserID || current.AccountNo != op.AccountNo) {
			return ErrConflict
		}
		var contact Contact
		err = s.Repo.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id=? AND customer_id=? AND id=? AND status='ACTIVE'",
				p.TenantID, op.CustomerID, op.ContactID).Take(&contact).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		return action(tx)
	})
}

// checkInvitationLink 在锁内复核不可变客户、联系人、账号及主体预留，不能以恢复覆盖归属。
// 参数：ctx为客户与操作已锁定的事务，p/op为已认证范围和原操作。
// 返回值：匹配才返回空；缺失、停用或任一归属变化时冲突。
func (s *Service) checkInvitationLink(ctx context.Context, p auth.Principal, op *Operation) error {
	var link account.IdentityLink
	err := s.Repo.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND platform_user_id=?", p.TenantID, op.PlatformUserID).Take(&link).Error
	if err != nil {
		return err
	}
	if link.CustomerID != op.CustomerID || link.ContactID == nil ||
		*link.ContactID != op.ContactID || link.AccountNo != op.AccountNo ||
		(link.Status != account.IdentityPending && link.Status != account.IdentityActive) {
		return ErrConflict
	}
	return nil
}

// reserveInvitationIdentity 在任何角色或绑定调用前持久独占真实主体并原子推进身份阶段。
// 参数：ctx/p为真实管理请求，op为原操作及平台回执，display为冻结联系人展示名。
// 返回值：预留或持久化错误；失败预留不会释放给另一客户或复活停用映射。
func (s *Service) reserveInvitationIdentity(ctx context.Context, p auth.Principal, op *Operation, display string) error {
	if _, err := CustomerBindingRef(op.CustomerID, op.PlatformUserID); err != nil {
		return err
	}
	return s.withInvitationTransaction(ctx, p, op, func(tx context.Context) error {
		err := s.checkInvitationLink(tx, p, op)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_, err = s.Accounts.Provision(tx, account.ProvisionCommand{TenantID: p.TenantID, CustomerID: op.CustomerID, ContactID: &op.ContactID, PlatformUserID: op.PlatformUserID, AccountNo: op.AccountNo, DisplayName: display})
		}
		if err != nil {
			return err
		}
		if err := s.checkInvitationLink(tx, p, op); err != nil {
			return err
		}
		if op.Stage == "PREPARED" {
			return s.advanceOperation(tx, p, op, "IDENTITY_READY", map[string]any{"platform_user_id": op.PlatformUserID, "account_no": op.AccountNo})
		}
		return nil
	})
}

// advanceReservedInvitation 在持锁复核归属后执行真实外部动作并推进原操作。
// 参数：ctx/p/op为持久管理范围，stage为目标阶段，action为具有原幂等键的外部调用。
// 返回值：真实回执或存储错误；网络未知保留既有预留，供原操作重试。
func (s *Service) advanceReservedInvitation(ctx context.Context, p auth.Principal, op *Operation, stage string, action func(context.Context) error) error {
	return s.withInvitationTransaction(ctx, p, op, func(tx context.Context) error {
		if err := s.checkInvitationLink(tx, p, op); err != nil {
			return err
		}
		if err := action(tx); err != nil {
			return err
		}
		return s.advanceOperation(tx, p, op, stage, map[string]any{})
	})
}
