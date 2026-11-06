package sourceauthority

import (
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"gorm.io/gorm"
	"time"
)

var ErrConflict = apperror.New(409, "PORTAL_CUSTOMER_AUTHORITY_CONFLICT", "客户目录权威已固定，不能直接切换来源")

// Claim 在原业务事务中串行目录来源，与成功业务写一起提交或回滚。
// 参数：tx为现有事务，tenant/mode/actor为受控服务坐标，now为服务器时间。
// 返回值：来源冲突或真实存储错误，不将数字客户碰撞当作同一来源。
func Claim(tx *gorm.DB, tenant, mode, actor string, now time.Time) error {
	if tx == nil || tenant == "" || (mode != "crm" && mode != "local") || actor == "" {
		return ErrConflict
	}
	if err := tx.Exec(`INSERT INTO portal_customer_authorities
(tenant_id,application_code,mode,first_used_at,actor_id,version)
VALUES(?,'customer_portal',?,?,?,1)
ON DUPLICATE KEY UPDATE tenant_id=VALUES(tenant_id)`, tenant, mode, now.UTC(), actor).Error; err != nil {
		return err
	}
	var row struct{ Mode string }
	result := tx.Raw("SELECT mode FROM portal_customer_authorities WHERE tenant_id=? AND application_code='customer_portal' FOR UPDATE", tenant).Scan(&row)
	if result.Error != nil {
		return result.Error
	}
	if row.Mode != mode {
		return ErrConflict
	}
	return nil
}
