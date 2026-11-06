package localidentity

import (
	"context"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
)

// customerReportScope 限定真实客户身份及本地报告账号，不给管理会话客户身份。
// 参数：ctx为认证上下文，actor为原客户边界，permission为精确读取或下载权限。
// 返回值：同时限定来源、真实账号与当前MANUAL项目授权的查询。
func (s *Service) customerReportScope(ctx context.Context, actor report.Actor, permission string) (*gorm.DB, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.TenantID != s.TenantID || p.TenantID != actor.TenantID || p.UserID != actor.AccountID || actor.CustomerID == 0 || len(p.Roles) != 1 || p.Roles[0] != "portal_customer" || !p.HasPermission(permission) {
		return nil, apperror.ErrForbidden
	}
	adapter := AccountAdapter{Repo: s.Repo, TenantID: s.TenantID, HMACKey: s.HMACKey, Now: s.now}
	if err := adapter.ValidateCustomer(ctx, actor.TenantID, actor.CustomerID, actor.AccountID, false); err != nil {
		return nil, err
	}
	q := s.Repo.tx(ctx).Model(&report.Request{}).Where("portal_report_requests.tenant_id=? AND portal_report_requests.customer_id=? AND portal_report_requests.account_id=?", actor.TenantID, actor.CustomerID, actor.AccountID)
	q = q.Where(`EXISTS (SELECT 1 FROM portal_local_report_sources source
WHERE source.tenant_id=portal_report_requests.tenant_id
 AND source.customer_id=portal_report_requests.customer_id
 AND source.request_id=portal_report_requests.id
 AND source.project_id=portal_report_requests.project_id
 AND source.account_id=? AND source.namespace='portal-local')`, actor.AccountID)
	q = q.Where(`EXISTS (SELECT 1 FROM portal_project_account_bindings binding
WHERE binding.tenant_id=portal_report_requests.tenant_id
 AND binding.customer_id=portal_report_requests.customer_id
 AND binding.project_id=portal_report_requests.project_id
 AND binding.account_id=? AND binding.source='MANUAL'
 AND binding.status='ACTIVE' AND binding.deleted_at IS NULL)`, actor.AccountID)
	return q, nil
}

// CustomerReports 查询当前账号获精确授权的本地真实报告，保留原分页输出。
// 参数：ctx/actor为真实客户会话，page/size为有界分页。
// 返回值：来源、账号和项目授权交集内的报告，不返回同客户其他账号报告。
func (s *Service) CustomerReports(ctx context.Context, actor report.Actor, page, size int) (pagination.Page[report.Request], error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	if page > 1000000 {
		return pagination.Page[report.Request]{}, ErrInvalid
	}
	result := pagination.Page[report.Request]{Items: []report.Request{}, Page: page, PageSize: size}
	query, err := s.customerReportScope(ctx, actor, "report.read")
	if err != nil {
		return result, err
	}
	if err := query.Count(&result.Total).Error; err != nil {
		return result, err
	}
	err = query.Order("portal_report_requests.id DESC").Offset((page - 1) * size).Limit(size).Find(&result.Items).Error
	return result, err
}

// AssertCustomerReport 对每次详情、grant和下载重验精确本地客户ACL。
// 参数：ctx/actor为认证客户，id为真实报告请求，permission为原路由权限。
// 返回值：来源、账号或当前项目授权不匹配则拒绝，既有安全下载链仍须继续通过。
func (s *Service) AssertCustomerReport(ctx context.Context, actor report.Actor, id uint64, permission string) error {
	if id == 0 {
		return ErrNotFound
	}
	query, err := s.customerReportScope(ctx, actor, permission)
	if err != nil {
		return err
	}
	var count int64
	if err := query.Where("portal_report_requests.id=?", id).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}
