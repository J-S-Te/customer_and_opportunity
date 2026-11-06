package localidentity

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var legacyCustomerTables = []string{"portal_identity_links", "portal_sessions", "portal_activation_contexts", "portal_project_snapshots", "portal_report_requests", "portal_feedbacks", "portal_filings", "portal_service_evaluations", "portal_customer_service_options"}

type Repository struct{ db *gorm.DB }

// NewRepository 绑定门户自己的业务库，不接受CRM跨库句柄。
// 参数：db 为已完成迁移的Portal数据库。
// 返回值：本地目录仓储。
func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// tx 取得当前业务事务共享句柄。
// 参数：ctx 为请求上下文。
// 返回值：带请求上下文的门户句柄。
func (r *Repository) tx(ctx context.Context) *gorm.DB {
	return database.FromContext(ctx, r.db).WithContext(ctx)
}

// WithTransaction 在同一门户事务中执行目录与账号变更。
// 参数：ctx 为请求，fn 为业务事务函数。
// 返回值：业务或提交错误。
func (r *Repository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

// CheckMode 在启用前复核唯一权威及旧的非空客户业务。
// 参数：ctx 为请求，tenant 为可信租户，mode 为受控模式。
// 返回值：允许该模式或明确来源冲突；存储故障失败关闭。
func (r *Repository) CheckMode(ctx context.Context, tenant string, mode Mode) error {
	if tenant == "" || (mode != ModeCRM && mode != ModeLocal) {
		return ErrInvalid
	}
	var row Authority
	err := r.tx(ctx).Where("tenant_id=? AND application_code=?", tenant, "customer_portal").Take(&row).Error
	if err == nil {
		if row.Mode != mode {
			return ErrAuthorityConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if mode == ModeCRM {
		return nil
	}
	for _, table := range legacyCustomerTables {
		var count int64
		if err := r.tx(ctx).Table(table).Where("tenant_id=? AND customer_id>0", tenant).Limit(1).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAuthorityConflict
		}
	}
	return nil
}

// ClaimMode 在首次真实业务写入时原子锁定目录权威。
// 参数：ctx 为业务事务，tenant/mode 为可信范围，actor/now 为审计坐标。
// 返回值：同来源幂等或冲突错误，不自动转换已有客户。
func (r *Repository) ClaimMode(ctx context.Context, tenant string, mode Mode, actor string, now time.Time) error {
	if err := r.CheckMode(ctx, tenant, mode); err != nil {
		return err
	}
	row := Authority{TenantID: tenant, ApplicationCode: "customer_portal", Mode: mode, FirstUsedAt: now.UTC(), ActorID: actor, Version: 1}
	if err := r.tx(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND application_code=?", tenant, "customer_portal").Take(&row).Error; err != nil {
		return err
	}
	if row.Mode != mode {
		return ErrAuthorityConflict
	}
	return nil
}
