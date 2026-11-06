package portalbootstrap

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// crmCustomerDirectory 用 CRM 本地客户表回答门户配置所需的客户存在性校验。
// 门户 capability 的内部路由只在 CRM 客户来源模式下开放（见 crmSourceOnly），
// 因此这里以 crm_customers 为权威目录，不再回退平台客户目录。
type crmCustomerDirectory struct {
	db *gorm.DB
}

func newCRMCustomerDirectory(db *gorm.DB) *crmCustomerDirectory {
	return &crmCustomerDirectory{db: db}
}

// crmCustomerExistsQuery 是可单测的校验口径：只有当前租户下未删除、未合并且
// 状态为 ACTIVE 的客户才算存在。已合并客户的开关本应跟随合并结果，不能再单独配置。
func crmCustomerExistsQuery(db *gorm.DB, tenantID string, customerID uint64) *gorm.DB {
	return db.Table("crm_customers").
		Where("tenant_id = ? AND id = ? AND status = 'ACTIVE' AND merged_into_id IS NULL AND deleted_at IS NULL", tenantID, customerID)
}

func (d *crmCustomerDirectory) Exists(ctx context.Context, tenantID string, customerID uint64) (bool, error) {
	if tenantID == "" || customerID == 0 {
		// 缺租户或零客户号不可能是真实客户，直接按不存在处理，不访问数据库。
		return false, nil
	}
	if d == nil || d.db == nil {
		// 失败关闭：未装配目录时不能把校验当成“通过”。
		return false, errors.New("customer directory is not configured")
	}
	var count int64
	if err := crmCustomerExistsQuery(d.db.WithContext(ctx), tenantID, customerID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
