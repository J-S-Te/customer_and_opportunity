// Package portalreportreference 仅暴露 Portal 报告水印所需的
// 有界身份，不导出 CRM 存储或登录凭据。
package portalreportreference

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

const Scope = "portal.report.download_identity.read"

type Identity struct {
	TenantID      string `json:"tenant_id"`
	CustomerID    uint64 `json:"customer_id"`
	AccountID     string `json:"account_id"`
	CustomerName  string `json:"customer_name"`
	AccountMasked string `json:"account_masked"`
}
type Reader interface {
	Resolve(context.Context, string, uint64, string) (Identity, error)
}
type GORMReader struct{ DB *gorm.DB }

func (r GORMReader) Resolve(ctx context.Context, tenant string, customerID uint64, accountID string) (Identity, error) {
	value := Identity{TenantID: tenant, CustomerID: customerID, AccountID: accountID}
	var record struct{ CustomerName, AccountNo string }
	if r.DB == nil {
		return value, apperror.New(503, "CRM_PORTAL_REPORT_IDENTITY_UNAVAILABLE", "report identity unavailable")
	}
	// ACTIVE 关联及 USED 邀请将记录的平台登录名绑定到
	// 同一客户和主体。映射缺失或停用时不得猜测。
	err := database.FromContext(ctx, r.DB).Table("crm_customers AS c").Select("c.name AS customer_name,i.account_no").Joins("JOIN crm_portal_identity_links l ON l.tenant_id=c.tenant_id AND l.customer_id=c.id AND l.deleted_at IS NULL AND l.status='ACTIVE'").Joins("JOIN crm_portal_invites i ON i.tenant_id=l.tenant_id AND i.customer_id=l.customer_id AND i.platform_user_id=l.platform_user_id AND i.deleted_at IS NULL AND i.status='USED'").Where("c.tenant_id=? AND c.id=? AND c.status='ACTIVE' AND c.deleted_at IS NULL AND l.platform_user_id=?", tenant, customerID, accountID).Order("i.used_at DESC,i.id DESC").Take(&record).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return value, apperror.New(http.StatusServiceUnavailable, "CRM_PORTAL_REPORT_IDENTITY_UNAVAILABLE", "report identity unavailable")
	}
	if err != nil || strings.TrimSpace(record.CustomerName) == "" || strings.TrimSpace(record.AccountNo) == "" {
		return value, apperror.New(http.StatusNotFound, "CRM_PORTAL_REPORT_IDENTITY_NOT_FOUND", "report identity unavailable")
	}
	value.CustomerName = record.CustomerName
	value.AccountMasked = mask(record.AccountNo)
	return value, nil
}
func mask(value string) string {
	r := []rune(value)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + strings.Repeat("*", len(r)-4) + string(r[len(r)-2:])
}
