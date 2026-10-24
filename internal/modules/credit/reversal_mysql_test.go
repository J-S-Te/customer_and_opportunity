package credit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCreditPaymentReversalRestoresStateAndAllowsResettlement(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_CREDIT_TEST_DSN"))
	if dsn == "" {
		t.Skip("CRM_CREDIT_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_customers (
		id BIGINT UNSIGNED NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
		status VARCHAR(16) NOT NULL, merged_into_id BIGINT UNSIGNED NULL, deleted_at DATETIME(3) NULL,
		credit_level CHAR(1) NOT NULL, credit_updated_at DATETIME(3) NULL,
		credit_change_source VARCHAR(16) NOT NULL DEFAULT 'INITIAL',
		consecutive_ontime_count INT UNSIGNED NOT NULL DEFAULT 0,
		consecutive_late_count INT UNSIGNED NOT NULL DEFAULT 0,
		last_payment_eval_at DATETIME(3) NULL, owner_user_id VARCHAR(64) NOT NULL DEFAULT ''
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_customer_change_logs (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
		customer_id BIGINT UNSIGNED NOT NULL, field_name VARCHAR(64) NOT NULL,
		before_json TEXT NOT NULL, after_json TEXT NOT NULL, reason VARCHAR(500) NOT NULL,
		operator_id VARCHAR(128) NOT NULL, request_id VARCHAR(128) NOT NULL,
		occurred_at DATETIME(3) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&creditPaymentRecord{}, &CreditLog{}, &RuleSettings{}); err != nil {
		t.Fatal(err)
	}
	tenant := "credit-reversal-e2e"
	if err = db.Exec(`DELETE FROM crm_customer_credit_payment_records WHERE tenant_id=?`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`DELETE FROM crm_customers WHERE tenant_id=?`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`INSERT INTO crm_customers(id,tenant_id,status,credit_level,consecutive_ontime_count,consecutive_late_count) VALUES(42,?,'ACTIVE','A',0,0)`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "e2e"})
	svc := NewService(db)
	due := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	paid := due
	completed := PaymentEvent{EventID: "credit-evt-1", EventType: PaymentEventCompleted, PaymentID: "credit-pay-1", CustomerID: 42, DueDate: due, PaidDate: &paid, DueAmount: "100.00", PaidAmount: "100.00", ContractNo: "E2E-1", PeriodNo: "1", SourceSystem: "settlement"}
	record, err := svc.ProcessPayment(ctx, completed)
	if err != nil {
		t.Fatalf("process completion: %v", err)
	}
	if record.Evaluation != "ONTIME" {
		t.Fatalf("evaluation=%s", record.Evaluation)
	}
	reversal := PaymentEvent{EventID: "credit-evt-1-rev", EventType: PaymentEventReversed, PaymentID: "credit-pay-1-rev", ReversalOfPaymentID: completed.PaymentID, CustomerID: 42, DueDate: due, PaidDate: &paid, DueAmount: "100.00", PaidAmount: "0.00", ContractNo: "E2E-1", PeriodNo: "1", SourceSystem: "settlement"}
	reversed, err := svc.ProcessPayment(ctx, reversal)
	if err != nil {
		t.Fatalf("process reversal: %v", err)
	}
	if reversed.Evaluation != "REVERSED" {
		t.Fatalf("reversal evaluation=%s", reversed.Evaluation)
	}
	var level string
	var onTime, late uint32
	var last *time.Time
	if err = db.Raw(`SELECT credit_level,consecutive_ontime_count,consecutive_late_count,last_payment_eval_at FROM crm_customers WHERE tenant_id=? AND id=?`, tenant, 42).Row().Scan(&level, &onTime, &late, &last); err != nil {
		t.Fatal(err)
	}
	if level != "A" || onTime != 0 || late != 0 || last != nil {
		t.Fatalf("customer after reversal level=%s ontime=%d late=%d last=%v", level, onTime, late, last)
	}
	if _, err = svc.ProcessPayment(ctx, reversal); err != nil {
		t.Fatalf("replayed reversal: %v", err)
	}
	// 冲销后的重新结算属于新付款事件，必须再次应用
	// 已恢复的计数器。
	resettled := completed
	resettled.EventID = "credit-evt-2"
	resettled.PaymentID = "credit-pay-2"
	if _, err = svc.ProcessPayment(ctx, resettled); err != nil {
		t.Fatalf("re-settle after reversal: %v", err)
	}
	if err = db.Raw(`SELECT consecutive_ontime_count FROM crm_customers WHERE tenant_id=? AND id=?`, tenant, 42).Row().Scan(&onTime); err != nil {
		t.Fatal(err)
	}
	if onTime != 1 {
		t.Fatalf("re-settled ontime=%d", onTime)
	}
}
