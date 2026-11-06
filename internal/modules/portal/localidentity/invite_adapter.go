package localidentity

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccountAdapter struct {
	Repo     *Repository
	TenantID string
	HMACKey  []byte
	Now      func() time.Time
}

// now 返回服务器时间，测试可注入时钟。
// 参数：无。
// 返回值：UTC当前时间。
func (a *AccountAdapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// Verify 校验真实本地邀请，不读取或伪造CRM邀请。
// 参数：ctx 为请求，token 为一次性邀请材料。
// 返回值：固定租户、客户及真实平台主体，过期或撤销则拒绝。
func (a *AccountAdapter) Verify(ctx context.Context, token string) (account.VerifiedInvite, error) {
	if a == nil || a.Repo == nil || a.TenantID == "" || len(a.HMACKey) < 32 || len(token) != 64 {
		return account.VerifiedInvite{}, ErrInvalid
	}
	var invite Invitation
	if err := a.Repo.tx(ctx).Where("tenant_id=? AND token_hash=? AND status='PENDING' AND expires_at>?", a.TenantID, keyedDigest(a.HMACKey, "local-invite", token), a.now()).Take(&invite).Error; err != nil {
		return account.VerifiedInvite{}, ErrNotFound
	}
	if err := a.ValidateCustomer(ctx, a.TenantID, invite.CustomerID, invite.PlatformUserID, true); err != nil {
		return account.VerifiedInvite{}, err
	}
	return account.VerifiedInvite{TenantID: a.TenantID, CustomerID: invite.CustomerID, ContactID: &invite.ContactID, ExpectedPlatformUserID: invite.PlatformUserID}, nil
}

// Consume 为直接调用保留真实事务消费，不将已撤销邀请复活。
// 参数：ctx 为请求，token/subject 为已验证邀请与OIDC主体。
// 返回值：真实消费或并发、范围错误。
func (a *AccountAdapter) Consume(ctx context.Context, token, subject string) error {
	return a.Repo.WithTransaction(ctx, func(tx context.Context) error { return a.ConsumeInTransaction(tx, token, subject) })
}

// ConsumeInTransaction 与账号映射激活及会话创建共享Portal事务。
// 参数：ctx 为原会话事务，token/subject 为已经过OIDC验证的绑定。
// 返回值：消费成功或精确重放；不匹配、停用、过期及存储失败均拒绝。
func (a *AccountAdapter) ConsumeInTransaction(ctx context.Context, token, subject string) error {
	var row Invitation
	if a == nil || a.Repo == nil || len(a.HMACKey) < 32 || len(token) != 64 {
		return ErrInvalid
	}
	if err := a.Repo.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND token_hash=?", a.TenantID, keyedDigest(a.HMACKey, "local-invite", token)).Take(&row).Error; err != nil {
		return ErrNotFound
	}
	if row.PlatformUserID != subject {
		return ErrInvalid
	}
	customer, err := a.Repo.lockCustomer(ctx, a.TenantID, row.CustomerID)
	if err != nil || customer.Status != "ACTIVE" {
		return ErrNotFound
	}
	if row.Status == "CONSUMED" {
		return a.ValidateCustomer(ctx, a.TenantID, row.CustomerID, subject, false)
	}
	if row.Status != "PENDING" || !row.ExpiresAt.After(a.now()) {
		return ErrInvalid
	}
	var link account.IdentityLink
	if err := a.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND status='ACTIVE'", a.TenantID, row.CustomerID, subject).Take(&link).Error; err != nil {
		return ErrNotFound
	}
	if link.ContactID == nil || *link.ContactID != row.ContactID || link.AccountNo != row.AccountNo {
		return ErrInvalid
	}
	var contact Contact
	if err := a.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND id=? AND status='ACTIVE'", a.TenantID, row.CustomerID, row.ContactID).Take(&contact).Error; err != nil {
		return ErrNotFound
	}
	result := a.Repo.tx(ctx).Model(&Invitation{}).Where("id=? AND tenant_id=? AND status='PENDING' AND version=?", row.ID, a.TenantID, row.Version).Updates(map[string]any{"status": "CONSUMED", "consumed_at": a.now(), "updated_at": a.now(), "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// ValidateCustomer 对每次客户认证和水印生成复核本地有效权威。
// 参数：ctx 为请求，tenant/customer/subject 为服务端边界，pending仅用于真实邀请回调。
// 返回值：有效本地客户与映射可继续，否则失败关闭。
func (a *AccountAdapter) ValidateCustomer(ctx context.Context, tenant string, customer uint64, subject string, pending bool) error {
	if a == nil || a.Repo == nil || tenant != a.TenantID || customer == 0 || subject == "" {
		return ErrNotFound
	}
	if err := a.Repo.CheckMode(ctx, tenant, ModeLocal); err != nil {
		return err
	}
	if _, err := a.Repo.activeCustomer(ctx, tenant, customer); err != nil {
		return err
	}
	statuses := []account.IdentityStatus{account.IdentityActive}
	if pending {
		statuses = append(statuses, account.IdentityPending)
	}
	var link account.IdentityLink
	if err := a.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND status IN ?", tenant, customer, subject, statuses).Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if link.ContactID == nil {
		return ErrNotFound
	}
	var contact Contact
	if err := a.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND id=? AND status='ACTIVE'", tenant, customer, *link.ContactID).Take(&contact).Error; err != nil {
		return ErrNotFound
	}
	var count int64
	q := a.Repo.tx(ctx).Model(&Invitation{}).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND contact_id=?", tenant, customer, subject, link.ContactID)
	if pending {
		q = q.Where("((status='PENDING' AND expires_at>?) OR status='CONSUMED')", a.now())
	} else {
		q = q.Where("status='CONSUMED'")
	}
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve 为报告水印实时读取本地权威名称及真实账号的脱敏值。
// 参数：ctx 为下载请求，tenant/customer/accountID 为原客户ACL上下文。
// 返回值：有界水印身份；不构造CRM USED事实或猜测账号。
func (a *AccountAdapter) Resolve(ctx context.Context, tenant string, customer uint64, accountID string) (portalreportreference.Identity, error) {
	if err := a.ValidateCustomer(ctx, tenant, customer, accountID, false); err != nil {
		return portalreportreference.Identity{}, err
	}
	value, err := a.Repo.activeCustomer(ctx, tenant, customer)
	if err != nil {
		return portalreportreference.Identity{}, err
	}
	var link account.IdentityLink
	if err := a.Repo.tx(ctx).Where("tenant_id=? AND customer_id=? AND platform_user_id=? AND status='ACTIVE'", tenant, customer, accountID).Take(&link).Error; err != nil {
		return portalreportreference.Identity{}, err
	}
	return portalreportreference.Identity{TenantID: tenant, CustomerID: customer, AccountID: accountID, CustomerName: value.Name, AccountMasked: mask(link.AccountNo)}, nil
}

// ResolveCustomerRef 精确解析本地引用并核对固定租户内的真实客户、主体与邀请权威。
// 参数：ctx/tenant/subject为已认证边界，ref为平台引用，pending仅用于真实邀请回调。
// 返回值：有效本地客户主键；CRM数字、错主体、错租户或失效映射均拒绝。
func (a *AccountAdapter) ResolveCustomerRef(ctx context.Context, tenant, subject, ref string, pending bool) (uint64, error) {
	customer, err := ParseCustomerBindingRef(ref, subject)
	if err != nil {
		return 0, err
	}
	if err := a.ValidateCustomer(ctx, tenant, customer, subject, pending); err != nil {
		return 0, err
	}
	return customer, nil
}
