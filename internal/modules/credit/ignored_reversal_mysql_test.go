package credit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

// TestIgnoredPaymentReversalUnblocksResettlement 验证关闭规则时的无效果付款可幂等撤销后重新结清。
// 参数：t为测试上下文，仅使用CRM_TEST_DSN隔离库。
// 返回值：忽略事件无法补偿、身份错配被接受或信用状态被覆盖时失败。
func TestIgnoredPaymentReversalUnblocksResettlement(t *testing.T) {
	db, svc, tenant := financeCreditFixture(t, 9842042)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "machine"})
	rules := RuleSettings{TenantID: tenant, GraceDays: 7, OnTimeThreshold: 2, LateThreshold: 2, LevelStep: 1, Enabled: false, UpdatedAt: time.Now().UTC()}
	if err := db.Create(&rules).Error; err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	paid := due
	completed := PaymentEvent{EventID: "ignored-complete", EventType: PaymentEventCompleted, PaymentID: "ignored-payment", CustomerID: 9842042, DueDate: due, PaidDate: &paid, DueAmount: "100.00", PaidAmount: "100.00", SourceSystem: "settlement", ContractNo: "IGNORED-1", PeriodNo: "1"}
	ignored, err := svc.ProcessPayment(ctx, completed)
	if err != nil || ignored.Evaluation != "IGNORED_RULE_DISABLED" {
		t.Fatalf("ignore completion: %+v %v", ignored, err)
	}
	if err = db.Model(&RuleSettings{}).Where("tenant_id=?", tenant).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	reversal := completed
	reversal.EventID = "ignored-reverse"
	reversal.PaymentID = "ignored-reversal"
	reversal.EventType = PaymentEventReversed
	reversal.ReversalOfPaymentID = completed.PaymentID
	reversal.PaidAmount = "0.00"
	for _, mutate := range []func(*PaymentEvent){
		func(v *PaymentEvent) { v.CustomerID++ }, func(v *PaymentEvent) { v.ContractNo = "OTHER" },
		func(v *PaymentEvent) { v.PeriodNo = "2" }, func(v *PaymentEvent) { v.SourceSystem = "erp" },
		func(v *PaymentEvent) { v.DueAmount = "101.00" }, func(v *PaymentEvent) { v.DueDate = v.DueDate.AddDate(0, 0, 1) },
		func(v *PaymentEvent) { v.PaidDate = nil },
	} {
		invalid := reversal
		mutate(&invalid)
		if _, err = svc.ProcessPayment(ctx, invalid); err == nil {
			t.Fatal("accepted mismatched original identity")
		}
	}
	for replay := 0; replay < 2; replay++ {
		result, err := svc.ProcessPayment(ctx, reversal)
		if err != nil || result.Evaluation != "REVERSED" {
			t.Fatalf("ignored reversal/replay: %+v %v", result, err)
		}
	}
	var original, compensation creditPaymentRecord
	if err = db.Where("tenant_id=? AND payment_id=?", tenant, completed.PaymentID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Where("tenant_id=? AND payment_id=?", tenant, reversal.PaymentID).Take(&compensation).Error; err != nil {
		t.Fatal(err)
	}
	if original.Evaluation != "IGNORED_RULE_DISABLED" || original.ReversedAt == nil || compensation.PrevLevel != nil || compensation.ResultLevel != nil {
		t.Fatalf("fabricated evaluation or snapshot: %+v %+v", original, compensation)
	}
	var logs int64
	db.Model(&CreditLog{}).Where("tenant_id=?", tenant).Count(&logs)
	if logs != 0 {
		t.Fatalf("no-effect reversal wrote credit change logs=%d", logs)
	}
	completed.EventID = "new-complete"
	completed.PaymentID = "new-payment"
	result, err := svc.ProcessPayment(ctx, completed)
	if err != nil || result.Evaluation != "ONTIME" {
		t.Fatalf("resettlement blocked: %+v %v", result, err)
	}
	var ontime int
	if err = db.Raw(`SELECT consecutive_ontime_count FROM crm_customers WHERE tenant_id=?`, tenant).Scan(&ontime).Error; err != nil {
		t.Fatal(err)
	}
	if ontime != 1 {
		t.Fatalf("new completion counter=%d", ontime)
	}
}

// TestIgnoredReversalPreservesLaterCreditState 验证无效果补偿不回写旧快照且兼容其他忽略原因。
// 参数：t为测试上下文，仅使用CRM_TEST_DSN隔离库。
// 返回值：补偿修改后续真实评价、客户不可用状态或生成信用日志时失败。
func TestIgnoredReversalPreservesLaterCreditState(t *testing.T) {
	for _, reason := range []string{"RULE_DISABLED", "CUSTOMER_UNAVAILABLE", "INCOMPLETE", "OUT_OF_ORDER"} {
		t.Run(reason, func(t *testing.T) {
			db, svc, tenant := financeCreditFixture(t, 9842043)
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "machine"})
			rules := RuleSettings{TenantID: tenant, GraceDays: 7, OnTimeThreshold: 100, LateThreshold: 100, LevelStep: 1, Enabled: reason != "RULE_DISABLED", UpdatedAt: time.Now().UTC()}
			if err := db.Create(&rules).Error; err != nil {
				t.Fatal(err)
			}
			due := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
			paid := due
			completed := PaymentEvent{EventID: "ignored", EventType: PaymentEventCompleted, PaymentID: "ignored", CustomerID: 9842043, DueDate: due, PaidDate: &paid, DueAmount: "100.00", PaidAmount: "100.00", SourceSystem: "settlement", ContractNo: "SAFE-1", PeriodNo: "1"}
			if reason == "CUSTOMER_UNAVAILABLE" {
				if err := db.Exec(`UPDATE crm_customers SET status='INACTIVE' WHERE tenant_id=?`, tenant).Error; err != nil {
					t.Fatal(err)
				}
			}
			if reason == "INCOMPLETE" {
				completed.PaidDate = nil
				completed.PaidAmount = "50.00"
			}
			if reason == "OUT_OF_ORDER" {
				later := due.AddDate(0, 0, 1)
				if err := db.Exec(`UPDATE crm_customers SET last_payment_eval_at=? WHERE tenant_id=?`, later, tenant).Error; err != nil {
					t.Fatal(err)
				}
			}
			original, err := svc.ProcessPayment(ctx, completed)
			if err != nil || original.Evaluation != "IGNORED_"+reason {
				t.Fatalf("ignore reason=%s: %+v %v", reason, original, err)
			}
			// 模拟无效果事实之后合法发生的人工信用变化，撤销不得恢复原快照。
			if err = db.Exec(`UPDATE crm_customers SET credit_level='C',consecutive_ontime_count=7 WHERE tenant_id=?`, tenant).Error; err != nil {
				t.Fatal(err)
			}
			reversal := completed
			reversal.EventType = PaymentEventReversed
			reversal.EventID = "reverse"
			reversal.PaymentID = "reverse"
			reversal.ReversalOfPaymentID = completed.PaymentID
			reversal.PaidAmount = "0.00"
			if _, err = svc.ProcessPayment(ctx, reversal); err != nil {
				t.Fatal(err)
			}
			var row struct {
				Level  string
				OnTime int
				Status string
			}
			if err = db.Raw(`SELECT credit_level AS level,consecutive_ontime_count AS on_time,status FROM crm_customers WHERE tenant_id=?`, tenant).Scan(&row).Error; err != nil {
				t.Fatal(err)
			}
			wantStatus := "ACTIVE"
			if reason == "CUSTOMER_UNAVAILABLE" {
				wantStatus = "INACTIVE"
			}
			if row.Level != "C" || row.OnTime != 7 || row.Status != wantStatus {
				t.Fatalf("overwrote later credit state: %+v", row)
			}
		})
	}
}

// TestEvaluatedReversalKeepsSnapshotAndLatestGuards 验证真实评价仍要求最新事件及当前快照吻合。
// 参数：t为测试上下文，仅使用CRM_TEST_DSN隔离库。
// 返回值：旧事件/信用状态变更后可被撤销、合法撤销失败时测试失败。
func TestEvaluatedReversalKeepsSnapshotAndLatestGuards(t *testing.T) {
	db, svc, tenant := financeCreditFixture(t, 9842044)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "machine"})
	rules := RuleSettings{TenantID: tenant, GraceDays: 7, OnTimeThreshold: 100, LateThreshold: 100, LevelStep: 1, Enabled: true, UpdatedAt: time.Now().UTC()}
	if err := db.Create(&rules).Error; err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	paid := due
	completed := PaymentEvent{EventID: "first", EventType: PaymentEventCompleted, PaymentID: "first", CustomerID: 9842044, DueDate: due, PaidDate: &paid, DueAmount: "100.00", PaidAmount: "100.00", SourceSystem: "settlement"}
	if _, err := svc.ProcessPayment(ctx, completed); err != nil {
		t.Fatal(err)
	}
	second := completed
	second.EventID = "second"
	second.PaymentID = "second"
	if _, err := svc.ProcessPayment(ctx, second); err != nil {
		t.Fatal(err)
	}
	reversal := completed
	reversal.EventType = PaymentEventReversed
	reversal.EventID = "reverse"
	reversal.PaymentID = "reverse"
	reversal.ReversalOfPaymentID = completed.PaymentID
	reversal.PaidAmount = "0.00"
	if _, err := svc.ProcessPayment(ctx, reversal); err == nil || !strings.Contains(err.Error(), "only the latest") {
		t.Fatalf("latest guard lost: %v", err)
	}
	reversal.ReversalOfPaymentID = second.PaymentID
	if err := db.Exec(`UPDATE crm_customers SET credit_level='C' WHERE tenant_id=?`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessPayment(ctx, reversal); err == nil || !strings.Contains(err.Error(), "credit state changed") {
		t.Fatalf("snapshot guard lost: %v", err)
	}
	if err := db.Exec(`UPDATE crm_customers SET credit_level='A' WHERE tenant_id=?`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		if _, err := svc.ProcessPayment(ctx, reversal); err != nil {
			t.Fatalf("legal evaluated reversal: %v", err)
		}
	}
	var count int
	if err := db.Raw(`SELECT consecutive_ontime_count FROM crm_customers WHERE tenant_id=?`, tenant).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("snapshot counter=%d", count)
	}
}
