package customer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// handoverGateTableDDL 与 migrations/000127_crm_handover_gate.up.sql 保持一致，
// 让隔离 MySQL 用例在没有预跑迁移时也能验证门闩行为。
const handoverGateTableDDL = `CREATE TABLE IF NOT EXISTS crm_handover_gate (
  tenant_id VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// 事务内门闩必须回滚整个写事务：即使事务外的预检说"未冻结"，
// 门闩行锁内的复核一旦查到 FROZEN，客户与幂等记录都不能落库。
func TestCustomerCreateRollsBackWhenTransactionGuardFindsFreezeOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("CRM_INTEGRATION_MYSQL_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(handoverGateTableDDL).Error; err != nil {
		t.Fatalf("ensure handover gate table: %v", err)
	}
	codec, err := security.NewSensitiveCodec([]byte("01234567890123456789012345678901"), []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tenant := "qa-customer-guard-" + now.Format("20060102150405.000000000")
	leaver := "leaver-" + now.Format("150405.000000000")
	t.Cleanup(func() {
		db.Exec("DELETE FROM crm_customers WHERE tenant_id = ?", tenant)
		db.Exec("DELETE FROM crm_customer_create_idempotency WHERE tenant_id = ?", tenant)
		db.Exec("DELETE FROM crm_handover_freeze WHERE tenant_id = ?", tenant)
		db.Exec("DELETE FROM crm_handover_gate WHERE tenant_id = ?", tenant)
	})
	// 冻结行在事务外已落库，但检查器桩故意报告"未冻结"，
	// 以模拟"预检之后才落库冻结"的竞态；最终防线必须是事务内的门闩复核。
	if err = db.Exec("INSERT INTO crm_handover_freeze (id, tenant_id, batch_id, request_id, user_id, status, frozen_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
		"01CRMFREEZE0000000000GATE1", tenant, "01CRMBATCH0000000000GATE1", "01CRMREQUEST000000000GATE1", leaver, "FROZEN", now, now, now).Error; err != nil {
		t.Fatalf("seed frozen handover: %v", err)
	}
	service := NewService(db, NewGORMRepository(db), audit.NewGORMWriter(db), codec)
	service.UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		TenantID: tenant, UserID: leaver, Roles: []string{"sales"}, ScopeMode: auth.ScopeAll,
		Permissions: map[string]struct{}{"customer.create": {}},
	})
	result, err := service.Create(ctx, CreateRequest{
		Name: "门闩验收客户" + now.Format("150405.0000"), UnifiedCreditCode: "91310000" + now.Format("1504050000000"),
		CustomerType: "ENTERPRISE", Industry: "IT", Region: "CN",
		Contacts: []ContactInput{{Name: "联系人", Phone: "13800138000", IsRegistration: true}},
		Reason:   "门闩回滚验收", IdempotencyKey: "guard-rollback-" + now.Format("150405000000000"),
	})
	if result != nil || !errors.Is(err, ErrOwnerFrozenByHandover) {
		t.Fatalf("create result=%#v err=%v, want ErrOwnerFrozenByHandover", result, err)
	}
	var customers, idempotency int64
	if err = db.Table("crm_customers").Where("tenant_id = ?", tenant).Count(&customers).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Table("crm_customer_create_idempotency").Where("tenant_id = ?", tenant).Count(&idempotency).Error; err != nil {
		t.Fatal(err)
	}
	if customers != 0 || idempotency != 0 {
		t.Fatalf("guard failure must roll back the whole transaction: customers=%d idempotency=%d", customers, idempotency)
	}
	// 门闩行由 upsert 自愈，随事务回滚后不留下半成品状态即可，不做额外断言。
}
