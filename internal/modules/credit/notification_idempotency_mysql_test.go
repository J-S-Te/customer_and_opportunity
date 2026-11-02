package credit

import (
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 校验真实唯一键 (tenant_id, source_event_id) + ON DUPLICATE KEY UPDATE id=id 下的
// 幂等语义：同一次等级变化重试只留一条，第二次不同的等级变化必须另推一条。
// 仅使用显式隔离的测试 DSN，未配置时跳过且不尝试连接。
func TestCreditNotificationIdempotencyAgainstIsolatedMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_CREDIT_TEST_DSN"))
	if dsn == "" {
		t.Skip("CRM_CREDIT_TEST_DSN is required; no connection attempted")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err = db.Exec(`CREATE TABLE IF NOT EXISTS crm_notifications (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		tenant_id VARCHAR(64) NOT NULL,
		created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
		created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
		deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
		source_event_id VARCHAR(256) NOT NULL, type VARCHAR(64) NOT NULL,
		opportunity_id BIGINT UNSIGNED NOT NULL, customer_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
		opportunity_version BIGINT UNSIGNED NOT NULL,
		opportunity_no VARCHAR(32) NOT NULL, opportunity_name VARCHAR(200) NOT NULL,
		request_id BIGINT UNSIGNED NOT NULL DEFAULT 0, request_no VARCHAR(32) NOT NULL,
		assignment_id BIGINT UNSIGNED NOT NULL DEFAULT 0, progress_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
		recipient_id VARCHAR(64) NOT NULL, recipient_kind VARCHAR(32) NOT NULL,
		title VARCHAR(200) NOT NULL, body VARCHAR(500) NOT NULL,
		target_path VARCHAR(500) NOT NULL, status VARCHAR(16) NOT NULL,
		read_at DATETIME(3) NULL,
		UNIQUE KEY uq_crm_notification_source (tenant_id, source_event_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		t.Fatal(err)
	}
	const tenant = "credit-notification-e2e"
	clean := func() error { return db.Exec(`DELETE FROM crm_notifications WHERE tenant_id=?`, tenant).Error }
	if err = clean(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := clean(); err != nil {
			t.Errorf("clean fixture notifications: %v", err)
		}
	})
	service := NewService(db)
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	notify := func(recipient, body string) {
		t.Helper()
		if err := service.notifyApplication(db, tenant, 42, 0, recipient, "CREDIT_RULE_CHANGED", "客户信用等级已自动调整", body, now); err != nil {
			t.Fatalf("notify %s %q: %v", recipient, body, err)
		}
	}
	count := func() int64 {
		t.Helper()
		var total int64
		if err := db.Table("crm_notifications").Where("tenant_id=? AND deleted_at IS NULL", tenant).Count(&total).Error; err != nil {
			t.Fatal(err)
		}
		return total
	}
	firstChange := "客户信用等级已由规则从 B 调整为 C。"
	secondChange := "客户信用等级已由规则从 C 调整为 D。"

	notify("owner-1", firstChange)
	notify("owner-1", firstChange)
	if got := count(); got != 1 {
		t.Fatalf("a retried change produced %d notifications, want 1", got)
	}
	notify("owner-1", secondChange)
	notify("owner-1", secondChange)
	if got := count(); got != 2 {
		t.Fatalf("the second level change produced %d notifications in total, want 2", got)
	}
	var bodies []string
	if err := db.Table("crm_notifications").Where("tenant_id=? AND recipient_id=?", tenant, "owner-1").Order("id ASC").Pluck("body", &bodies).Error; err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != firstChange || bodies[1] != secondChange {
		t.Fatalf("recipient inbox bodies = %q, want both level changes", bodies)
	}
	notify("director-1", firstChange)
	if got := count(); got != 3 {
		t.Fatalf("a different recipient must get its own notification, total = %d", got)
	}
}
