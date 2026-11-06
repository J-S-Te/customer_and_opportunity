package localidentity

import (
	"context"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
)

type AccountSummary struct {
	PlatformUserID string                 `json:"platform_user_id"`
	AccountNo      string                 `json:"account_no"`
	ContactID      *uint64                `json:"contact_id"`
	Status         account.IdentityStatus `json:"status"`
	Version        uint64                 `json:"version"`
}

// ListAccounts 返回本地客户的真实平台映射及并发版本。
// 参数：ctx为客户目录管理员，customer为本地客户。
// 返回值：有界账号元数据，不授予其客户业务查询能力。
func (s *Service) ListAccounts(ctx context.Context, customer uint64) ([]AccountSummary, error) {
	p, err := s.principal(ctx, PermissionCustomerManage, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.activeCustomer(ctx, p.TenantID, customer); err != nil {
		return nil, err
	}
	rows := []AccountSummary{}
	err = s.Repo.tx(ctx).Model(&account.IdentityLink{}).Select("platform_user_id,account_no,contact_id,status,version").Where("tenant_id=? AND customer_id=?", p.TenantID, customer).Order("id").Limit(100).Scan(&rows).Error
	return rows, err
}

// ListInvitations 返回本地真实邀请状态，不返回邀请秘密或CRM状态。
// 参数：ctx为邀请管理员，customer为本地客户。
// 返回值：最近一百条本地邀请记录。
func (s *Service) ListInvitations(ctx context.Context, customer uint64) ([]Invitation, error) {
	p, err := s.principal(ctx, PermissionInviteManage, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.activeCustomer(ctx, p.TenantID, customer); err != nil {
		return nil, err
	}
	rows := []Invitation{}
	err = s.Repo.tx(ctx).Where("tenant_id=? AND customer_id=?", p.TenantID, customer).Order("id DESC").Limit(100).Find(&rows).Error
	return rows, err
}

type ProjectAccessSummary struct {
	ProjectID string `json:"project_id"`
	AccountID string `json:"account_id"`
	Version   uint64 `json:"version"`
	Enabled   bool   `json:"enabled"`
}

// ListProjectAccess 列出精确客户的MANUAL授权及并发版本。
// 参数：ctx为项目管理员，customer为本地客户。
// 返回值：最多一百条授权元数据，不授予客户业务读取能力。
func (s *Service) ListProjectAccess(ctx context.Context, customer uint64) ([]ProjectAccessSummary, error) {
	p, err := s.principal(ctx, PermissionProjectManage, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.activeCustomer(ctx, p.TenantID, customer); err != nil {
		return nil, err
	}
	rows := []ProjectAccessSummary{}
	err = s.Repo.tx(ctx).Table("portal_project_account_bindings").Select("project_id,account_id,version,status='ACTIVE' AS enabled").Where("tenant_id=? AND customer_id=? AND source='MANUAL' AND deleted_at IS NULL", p.TenantID, customer).Order("id").Limit(100).Scan(&rows).Error
	return rows, err
}
