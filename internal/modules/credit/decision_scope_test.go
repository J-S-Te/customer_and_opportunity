package credit

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// creditApprover 用途：构造带审批角色和指定数据范围的固定租户主体。
// 参数：userID 为审批人；scope 为范围模式；organizations 为可见组织。
// 返回值：审批测试主体。
func creditApprover(userID string, scope auth.ScopeMode, organizations ...string) auth.Principal {
	return creditApproverInTenant("tenant-a", userID, scope, organizations...)
}

// creditApproverInTenant 用途：构造与动态测试租户一致的审批主体。
// 参数：tenant 为测试租户；userID 为审批人；scope 与 organizations 为数据范围。
// 返回值：带销售总监角色的测试主体。
func creditApproverInTenant(tenant, userID string, scope auth.ScopeMode, organizations ...string) auth.Principal {
	return auth.Principal{TenantID: tenant, UserID: userID, Roles: []string{"sales_director"}, ScopeMode: scope, OrganizationIDs: organizations}
}

// TestPendingEmbedsCustomerScopeSubquery 用途：验证信用待办以客户可见性子查询裁剪结果。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
func TestPendingEmbedsCustomerScopeSubquery(t *testing.T) {
	cases := []struct {
		name      string
		principal auth.Principal
		want      []string
		reject    []string
	}{
		{
			name:      "self",
			principal: creditApprover("director-a", auth.ScopeSelf),
			want:      []string{"crm_customers.owner_user_id = 'director-a'"},
			reject:    []string{"crm_customers.owner_org_id"},
		},
		{
			name:      "org",
			principal: creditApprover("director-a", auth.ScopeOrg, "org-a", "org-b"),
			want:      []string{"crm_customers.owner_org_id IN"},
			reject:    []string{"crm_customers.owner_user_id"},
		},
		{
			name:      "org-empty",
			principal: creditApprover("director-a", auth.ScopeOrg),
			want:      []string{"1 = 0"},
			reject:    []string{"crm_customers.owner_user_id", "crm_customers.owner_org_id"},
		},
		{
			name:      "all",
			principal: creditApprover("admin-a", auth.ScopeAll),
			want:      nil,
			reject:    []string{"crm_customers.owner_user_id", "crm_customers.owner_org_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newCreditSQLRecorder()
			service := NewService(creditDryRunDB(t, recorder))
			ctx := auth.WithPrincipal(context.Background(), tc.principal)
			// Scan 在 DryRun 下用 ErrDryRunModeUnsupported 表示“只编译不执行”，
			// SQL 已被 recorder 捕获；其它错误必须失败。
			if _, err := service.Pending(ctx); err != nil && !errors.Is(err, gorm.ErrDryRunModeUnsupported) {
				t.Fatalf("DryRun Pending 返回错误：%v", err)
			}
			query := recorder.firstQueryContaining(t,
				"FROM crm_customer_credit_applications AS application",
				"application.customer_id IN (SELECT",
			)
			if !strings.Contains(query, "LEFT JOIN crm_customers AS customer") {
				t.Fatalf("待办箱客户信息 join 被改动：%q", query)
			}
			if !strings.Contains(query, "application.tenant_id='tenant-a' AND application.status='PENDING'") {
				t.Fatalf("待办箱丢失申请自身的租户/状态条件：%q", query)
			}
			if !strings.Contains(query, "crm_customers.tenant_id = 'tenant-a'") || !strings.Contains(query, "crm_customers.deleted_at IS NULL") {
				t.Fatalf("待办箱子查询丢失客户租户/删除条件：%q", query)
			}
			for _, want := range tc.want {
				if !strings.Contains(query, want) {
					t.Fatalf("待办箱缺少客户范围条件 %q：%q", want, query)
				}
			}
			for _, reject := range tc.reject {
				if strings.Contains(query, reject) {
					t.Fatalf("待办箱出现不该有的范围条件 %q：%q", reject, query)
				}
			}
		})
	}
}

// TestDecideCustomerLockAppliesCustomerScope 用途：验证信用审批的客户行锁保留数据范围谓词。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
func TestDecideCustomerLockAppliesCustomerScope(t *testing.T) {
	cases := []struct {
		name      string
		principal auth.Principal
		want      []string
		reject    []string
	}{
		{
			name:      "self",
			principal: creditApprover("director-a", auth.ScopeSelf),
			want:      []string{"crm_customers.owner_user_id = 'director-a'"},
			reject:    []string{"crm_customers.owner_org_id"},
		},
		{
			name:      "org",
			principal: creditApprover("director-a", auth.ScopeOrg, "org-a"),
			want:      []string{"crm_customers.owner_org_id IN"},
			reject:    []string{"crm_customers.owner_user_id"},
		},
		{
			name:      "org-empty",
			principal: creditApprover("director-a", auth.ScopeOrg),
			want:      []string{"1 = 0"},
			reject:    []string{"crm_customers.owner_user_id", "crm_customers.owner_org_id"},
		},
		{
			name:      "all",
			principal: creditApprover("admin-a", auth.ScopeAll),
			want:      nil,
			reject:    []string{"crm_customers.owner_user_id", "crm_customers.owner_org_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newCreditSQLRecorder()
			db := creditDryRunDB(t, recorder)
			var customer struct {
				ID          uint64
				CreditLevel string
			}
			// DryRun 不执行语句，因此这里既没有命中行也没有 ErrRecordNotFound；
			// 用例断言的是下发 SQL 的锁与范围谓词，不是执行结果。
			if err := lockScopedCreditCustomer(db, tc.principal, 7, &customer); err != nil {
				t.Fatalf("DryRun 客户锁错误=%v", err)
			}
			query := recorder.firstQueryContaining(t, "FROM `crm_customers`", "FOR UPDATE")
			for _, want := range append([]string{"crm_customers.tenant_id = 'tenant-a'", "crm_customers.deleted_at IS NULL", "crm_customers.status='ACTIVE'", "crm_customers.id=7"}, tc.want...) {
				if !strings.Contains(query, want) {
					t.Fatalf("客户锁缺少条件 %q：%q", want, query)
				}
			}
			for _, reject := range tc.reject {
				if strings.Contains(query, reject) {
					t.Fatalf("客户锁出现不该有的范围条件 %q：%q", reject, query)
				}
			}
		})
	}
}

// creditDecideErrorCode 用途：提取信用审批业务错误码以校验稳定拒绝契约。
// 参数：t 为测试上下文；err 为审批返回错误。
// 返回值：业务错误码；非业务错误时终止测试。
func creditDecideErrorCode(t *testing.T, err error) string {
	t.Helper()
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("期望业务错误，实际 err=%v", err)
	}
	return appErr.Code
}

// TestCreditDecideEnforcesCustomerScopeOnMySQL 用途：在显式隔离 MySQL 上验证范围外审批失败关闭。
// 参数：t 为该行为的测试上下文。
// 返回值：无；行为与预期不符时测试失败。
func TestCreditDecideEnforcesCustomerScopeOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRM_CREDIT_TEST_DSN"))
	if dsn == "" {
		t.Skip("CRM_CREDIT_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS crm_customers (
			id BIGINT UNSIGNED NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			status VARCHAR(16) NOT NULL, merged_into_id BIGINT UNSIGNED NULL, deleted_at DATETIME(3) NULL,
			credit_level CHAR(1) NOT NULL, credit_updated_at DATETIME(3) NULL,
			credit_change_source VARCHAR(16) NOT NULL DEFAULT 'INITIAL',
			consecutive_ontime_count INT UNSIGNED NOT NULL DEFAULT 0,
			consecutive_late_count INT UNSIGNED NOT NULL DEFAULT 0,
			last_payment_eval_at DATETIME(3) NULL,
			owner_user_id VARCHAR(64) NOT NULL DEFAULT '', owner_org_id VARCHAR(64) NOT NULL DEFAULT ''
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS crm_customer_credit_applications (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			customer_id BIGINT UNSIGNED NOT NULL, applicant_id VARCHAR(64) NOT NULL,
			idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
			from_level CHAR(1) NOT NULL, target_level CHAR(1) NOT NULL,
			reason VARCHAR(500) NOT NULL, status VARCHAR(16) NOT NULL,
			opinion VARCHAR(500) NOT NULL DEFAULT '', decided_by VARCHAR(64) NOT NULL DEFAULT '',
			decided_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
			version BIGINT UNSIGNED NOT NULL DEFAULT 1, pending_customer_id BIGINT UNSIGNED NULL,
			approval_instance_id BIGINT UNSIGNED NOT NULL DEFAULT 0
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS crm_approval_instances (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			biz_type VARCHAR(64) NOT NULL, business_id BIGINT UNSIGNED NOT NULL,
			status VARCHAR(16) NOT NULL, current_task_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
			created_by VARCHAR(64) NOT NULL, decided_by VARCHAR(64) NOT NULL DEFAULT '',
			decided_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
			version BIGINT UNSIGNED NOT NULL DEFAULT 1
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS crm_approval_tasks (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			instance_id BIGINT UNSIGNED NOT NULL, task_code VARCHAR(64) NOT NULL,
			assignee_role VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL,
			decided_by VARCHAR(64) NOT NULL DEFAULT '', decided_at DATETIME(3) NULL,
			created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS crm_customer_change_logs (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			customer_id BIGINT UNSIGNED NOT NULL, field_name VARCHAR(64) NOT NULL,
			before_json TEXT NOT NULL, after_json TEXT NOT NULL, reason VARCHAR(500) NOT NULL,
			operator_id VARCHAR(128) NOT NULL, request_id VARCHAR(128) NOT NULL,
			occurred_at DATETIME(3) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS crm_customer_credit_logs (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			customer_id BIGINT UNSIGNED NOT NULL, application_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
			payment_id VARCHAR(128) NOT NULL DEFAULT '', event_id VARCHAR(128) NOT NULL DEFAULT '',
			from_level CHAR(1) NOT NULL, to_level CHAR(1) NOT NULL, source VARCHAR(16) NOT NULL,
			operator_id VARCHAR(64) NOT NULL DEFAULT '', reason VARCHAR(500) NOT NULL,
			occurred_at DATETIME(3) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS crm_notifications (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
			created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
			source_event_id VARCHAR(191) NOT NULL, type VARCHAR(64) NOT NULL,
			opportunity_id BIGINT UNSIGNED NOT NULL DEFAULT 0, customer_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
			opportunity_version BIGINT UNSIGNED NOT NULL DEFAULT 0, opportunity_no VARCHAR(32) NOT NULL DEFAULT '',
			opportunity_name VARCHAR(200) NOT NULL DEFAULT '', request_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
			request_no VARCHAR(32) NOT NULL DEFAULT '', assignment_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
			progress_id BIGINT UNSIGNED NOT NULL DEFAULT 0, recipient_id VARCHAR(64) NOT NULL,
			recipient_kind VARCHAR(32) NOT NULL, title VARCHAR(200) NOT NULL, body VARCHAR(500) NOT NULL,
			target_path VARCHAR(500) NOT NULL, status VARCHAR(16) NOT NULL,
			created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
			version BIGINT UNSIGNED NOT NULL DEFAULT 1
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, statement := range ddl {
		if err = db.Exec(statement).Error; err != nil {
			t.Fatalf("准备隔离测试表失败：%v", err)
		}
	}
	now := time.Now().UTC()
	tenant := "qa-credit-decide-scope-" + now.Format("20060102150405.000000000")
	base := uint64(now.UnixMicro())
	customerOrgA := base
	customerOrgB := base + 1
	customerInactive := base + 2
	applicationOrgA := base + 10
	applicationOrgB := base + 11
	applicationInactive := base + 12
	instanceOrgA := base + 20
	instanceOrgB := base + 21
	instanceInactive := base + 22
	tables := []string{"crm_customer_credit_applications", "crm_approval_tasks", "crm_approval_instances", "crm_customer_credit_logs", "crm_customer_change_logs", "crm_notifications", "crm_customers"}
	t.Cleanup(func() {
		for _, table := range tables {
			_ = db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant).Error
		}
	})
	if err = db.Exec(`INSERT INTO crm_customers(id,tenant_id,status,credit_level,owner_user_id,owner_org_id) VALUES
		(?,?,'ACTIVE','B','sales-a','org-a'),(?,?,'ACTIVE','B','sales-b','org-b'),(?,?,'INACTIVE','B','sales-a','org-a')`,
		customerOrgA, tenant, customerOrgB, tenant, customerInactive, tenant).Error; err != nil {
		t.Fatal(err)
	}
	// applicationInactive 指向 org-a 组织内、但已非 ACTIVE 的客户：范围可见，状态失效。
	for _, row := range []struct {
		applicationID, customerID, instanceID uint64
	}{{applicationOrgA, customerOrgA, instanceOrgA}, {applicationOrgB, customerOrgB, instanceOrgB}, {applicationInactive, customerInactive, instanceInactive}} {
		if err = db.Exec(`INSERT INTO crm_approval_instances(id,tenant_id,biz_type,business_id,status,created_by,created_at,updated_at,version)
			VALUES(?,?,'CREDIT_ADJUSTMENT',?,'PENDING','sales-a',?,?,1)`, row.instanceID, tenant, row.applicationID, now, now).Error; err != nil {
			t.Fatal(err)
		}
		if err = db.Exec(`INSERT INTO crm_approval_tasks(tenant_id,instance_id,task_code,assignee_role,status,created_at,updated_at)
			VALUES(?,?,'SALES_DIRECTOR_APPROVE','sales_director','PENDING',?,?)`, tenant, row.instanceID, now, now).Error; err != nil {
			t.Fatal(err)
		}
		if err = db.Exec(`INSERT INTO crm_customer_credit_applications(id,tenant_id,customer_id,applicant_id,idempotency_key,from_level,target_level,reason,status,created_at,updated_at,version,pending_customer_id,approval_instance_id)
			VALUES(?,?,?,'sales-a',?,'B','A','客户信用等级调整申请','PENDING',?,?,1,?,?)`, row.applicationID, tenant, row.customerID, "idem-"+now.Format("150405.000000"), now, now, row.customerID, row.instanceID).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	level := func(customerID uint64) string {
		var value string
		if err := db.Raw("SELECT credit_level FROM crm_customers WHERE tenant_id=? AND id=?", tenant, customerID).Row().Scan(&value); err != nil {
			t.Fatalf("读取客户 %d 信用等级失败：%v", customerID, err)
		}
		return value
	}
	applicationStatus := func(applicationID uint64) string {
		var value string
		if err := db.Raw("SELECT status FROM crm_customer_credit_applications WHERE tenant_id=? AND id=?", tenant, applicationID).Row().Scan(&value); err != nil {
			t.Fatalf("读取申请 %d 状态失败：%v", applicationID, err)
		}
		return value
	}
	// 1) 空组织集合按无权限处理，不能退化成租户全量。
	emptyOrg := auth.WithPrincipal(context.Background(), creditApproverInTenant(tenant, "director-a", auth.ScopeOrg))
	if _, err := service.Decide(emptyOrg, applicationOrgA, true, DecisionRequest{Version: 1, Opinion: "同意调整"}); err == nil || creditDecideErrorCode(t, err) != "CRM_CREDIT_APPLICATION_NOT_FOUND" {
		t.Fatalf("空组织集合审批 err=%v, want 404 CRM_CREDIT_APPLICATION_NOT_FOUND", err)
	}
	// 2) 跨组织审批人同样不可见。
	otherOrg := auth.WithPrincipal(context.Background(), creditApproverInTenant(tenant, "director-b", auth.ScopeOrg, "org-b"))
	if _, err := service.Decide(otherOrg, applicationOrgA, true, DecisionRequest{Version: 1, Opinion: "同意调整"}); err == nil || creditDecideErrorCode(t, err) != "CRM_CREDIT_APPLICATION_NOT_FOUND" {
		t.Fatalf("跨组织审批 err=%v, want 404 CRM_CREDIT_APPLICATION_NOT_FOUND", err)
	}
	if got := level(customerOrgA); got != "B" {
		t.Fatalf("越权审批改动了客户信用等级：%s", got)
	}
	if got := applicationStatus(applicationOrgA); got != "PENDING" {
		t.Fatalf("越权审批改动了申请状态：%s", got)
	}
	// 3) 本组织审批人正常通过，信用等级落库。
	orgA := auth.WithPrincipal(context.Background(), creditApproverInTenant(tenant, "director-a", auth.ScopeOrg, "org-a"))
	decided, err := service.Decide(orgA, applicationOrgA, true, DecisionRequest{Version: 1, Opinion: "同意调整"})
	if err != nil {
		t.Fatalf("本组织审批失败：%v", err)
	}
	if decided.Status != "APPROVED" {
		t.Fatalf("审批结果=%s, want APPROVED", decided.Status)
	}
	if got := level(customerOrgA); got != "A" {
		t.Fatalf("本组织审批后信用等级=%s, want A", got)
	}
	// 4) 反向验证：org-a 审批人不能审批 org-b 客户的申请，且对方数据不变。
	if _, err := service.Decide(orgA, applicationOrgB, true, DecisionRequest{Version: 1, Opinion: "同意调整"}); err == nil || creditDecideErrorCode(t, err) != "CRM_CREDIT_APPLICATION_NOT_FOUND" {
		t.Fatalf("跨组织反向审批 err=%v, want 404 CRM_CREDIT_APPLICATION_NOT_FOUND", err)
	}
	if got := level(customerOrgB); got != "B" {
		t.Fatalf("跨组织审批改动了 org-b 客户信用等级：%s", got)
	}
	// 5) 可见但已失效的客户仍按既有契约回 409，不能被范围闸门误折叠成 404。
	if _, err := service.Decide(orgA, applicationInactive, true, DecisionRequest{Version: 1, Opinion: "同意调整"}); err == nil || creditDecideErrorCode(t, err) != "CRM_CREDIT_APPLICATION_INVALIDATED" {
		t.Fatalf("失效客户审批 err=%v, want 409 CRM_CREDIT_APPLICATION_INVALIDATED", err)
	}
	if got := applicationStatus(applicationInactive); got != "PENDING" {
		t.Fatalf("失效客户申请状态被改动：%s", got)
	}
	if got := applicationStatus(applicationOrgB); got != "PENDING" {
		t.Fatalf("跨组织审批改动了 org-b 申请状态：%s", got)
	}
}
