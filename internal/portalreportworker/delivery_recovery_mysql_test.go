package portalreportworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// recoveryProject 模拟按原事件持久化幂等的外部报告端口，不调用真实Provider。
type recoveryProject struct {
	err         error
	ids         map[string]string
	conflicting bool
}

// Submit 按事件幂等返回受理身份，用于驱动真实Worker和数据库状态机。
// 参数：ctx为任务上下文，event为真实领取的持久化事件。
// 返回值：稳定下游ID或指定临时错误；冲突开关用于验证错绑拒绝。
func (p *recoveryProject) Submit(ctx context.Context, event report.Outbox) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	if p.conflicting {
		return "wrong-downstream", nil
	}
	if p.ids == nil {
		p.ids = map[string]string{}
	}
	if p.ids[event.EventID] == "" {
		p.ids[event.EventID] = "external-" + event.EventID
	}
	return p.ids[event.EventID], nil
}

type recoveryCrashStore struct{ workerStore }

// sent 模拟受理绑定已提交但SENT写入前进程中断的窗口。
// 参数：ctx为任务上下文，event为领取事件，workerID为租约持有者，now为当前时间。
// 返回值：固定故障；不写入真实Outbox，其余存储方法仍调用真实数据库。
func (s recoveryCrashStore) sent(ctx context.Context, event report.Outbox, workerID string, now time.Time) error {
	return errors.New("injected process crash before sent")
}

// openRecoveryWorkerMySQL 准备本任务独立空库及最小实际数据表。
// 参数：t为测试上下文。
// 返回值：隔离数据库连接；未配置时跳过，非本机测试入口拒绝连接。
func openRecoveryWorkerMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("PORTAL_REPORT_TEST_DSN")
	if raw == "" {
		t.Skip("PORTAL_REPORT_TEST_DSN required; no connection attempted")
	}
	cfg, err := drivermysql.ParseDSN(raw)
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.DBName, "qa_flow02_portal_") || !(strings.HasPrefix(cfg.Addr, "127.0.0.1:") || strings.HasPrefix(cfg.Addr, "localhost:")) {
		t.Fatal("requires localhost disposable qa_flow02_portal_ schema")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.MultiStatements = false
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated MySQL failed")
	}
	schema := fmt.Sprintf("qa_flow02_portal_worker_%d", time.Now().UnixNano())
	if err = admin.Exec("CREATE DATABASE `" + schema + "`").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + schema + "`").Error; err != nil {
			t.Error(err)
		}
		conn, _ := admin.DB()
		_ = conn.Close()
	})
	cfg.DBName = schema
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open disposable worker schema failed")
	}
	t.Cleanup(func() { conn, _ := db.DB(); _ = conn.Close() })
	if err = db.AutoMigrate(&report.Request{}, &report.Outbox{}, &report.StatusEvent{}, &report.Notification{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE portal_customer_authorities (tenant_id VARCHAR(64) NOT NULL, application_code VARCHAR(64) NOT NULL, mode VARCHAR(16) NOT NULL, first_used_at DATETIME(3) NOT NULL, actor_id VARCHAR(128) NOT NULL, version BIGINT UNSIGNED NOT NULL, PRIMARY KEY(tenant_id,application_code))`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

// recoveryWorkerFixture 创建待投递申请及真实Worker依赖。
// 参数：t为测试上下文，db为本任务隔离连接。
// 返回值：申请、事件、领域服务、真实存储及外部端口替身。
func recoveryWorkerFixture(t *testing.T, db *gorm.DB) (report.Request, report.Outbox, *report.Service, *outboxStore, *recoveryProject) {
	t.Helper()
	now := time.Now().UTC()
	value := report.Request{ActorModel: report.ActorModel{TenantID: "recovery", CreatedBy: "account", UpdatedBy: "account", Version: 1}, RequestNo: "report-1", ProjectID: "project", CustomerID: 9, AccountID: "account", ReportType: "FINAL", Reason: "recovery", Status: report.StatusSubmitted, SubmittedAt: now, IdempotencyKey: "request-key", RequestHash: strings.Repeat("a", 64)}
	if err := db.Create(&value).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"request_id": value.ID, "request_no": value.RequestNo, "project_id": value.ProjectID, "customer_id": value.CustomerID, "report_type": value.ReportType, "reason": value.Reason})
	event := report.Outbox{TenantID: value.TenantID, AggregateID: value.ID, EventType: report.OutboxEventSubmitted, EventID: "delivery-event", Status: report.OutboxPending, Payload: raw, CreatedAt: now}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	service := report.NewService(report.NewGORMRepository(db), nil, nil, nil, workerClock{}, nil)
	return value, event, service, newOutboxStore(db, service), &recoveryProject{}
}

// applyRecoveryCallback 通过真实领域入口推进已受理申请。
// 参数：t为测试上下文，service为领域服务，value为原申请，downstream为真实受理身份。
// 返回值：无；回调拒绝时终止测试。
func applyRecoveryCallback(t *testing.T, service *report.Service, value report.Request, downstream string) {
	t.Helper()
	cb := report.Callback{TenantID: value.TenantID, RequestID: value.ID, CustomerID: value.CustomerID, ProjectID: value.ProjectID, DownstreamRequestID: downstream, Version: 1, IdempotencyKey: "callback", Status: report.StatusApprovedProcessing, ApprovalResult: "APPROVED"}
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
}

// TestWorkerRecoversDeadLetterOnMySQL 验证远端故障死信经管理员恢复后可受理和回调。
// 参数：t为测试上下文。
// 返回值：无；恢复未闭环、重复受理或重复通知时失败。
func TestWorkerRecoversDeadLetterOnMySQL(t *testing.T) {
	db := openRecoveryWorkerMySQL(t)
	value, event, service, store, project := recoveryWorkerFixture(t, db)
	db.Model(&event).Update("retry_count", 6)
	project.err = errors.New("temporary external outage")
	worker := NewWorker(store, service, project, Config{WorkerID: "worker", LeaseDuration: time.Minute, BatchSize: 1})
	if n, err := worker.RunOnce(context.Background()); n != 1 || err == nil {
		t.Fatalf("outage: n=%d err=%v", n, err)
	}
	if err := service.RetryDeliveryFailure(context.Background(), value.TenantID, event.EventID); err != nil {
		t.Fatal(err)
	}
	project.err = nil
	if n, err := worker.RunOnce(context.Background()); n != 1 || err != nil {
		t.Fatalf("recovered: n=%d err=%v", n, err)
	}
	applyRecoveryCallback(t, service, value, project.ids[event.EventID])
	var current report.Request
	db.First(&current, value.ID)
	var stored report.Outbox
	db.First(&stored, event.ID)
	var notices, events int64
	db.Model(&report.Notification{}).Count(&notices)
	db.Model(&report.StatusEvent{}).Count(&events)
	if current.Status != report.StatusApprovedProcessing || stored.Status != report.OutboxSent || len(project.ids) != 1 || notices != 1 || events != 4 {
		t.Fatalf("recovery: request=%s outbox=%s accepted=%d notices=%d events=%d", current.Status, stored.Status, len(project.ids), notices, events)
	}
}

// TestWorkerCrashAfterBindingConvergesOnMySQL 验证SENT崩溃窗口和回调先到后重投最终收敛。
// 参数：t为测试上下文。
// 返回值：无；回退进度、错绑、重复事件或无限PROCESSING时失败。
func TestWorkerCrashAfterBindingConvergesOnMySQL(t *testing.T) {
	for _, kind := range []string{"replay", "exhausted_outage", "wrong_binding"} {
		t.Run(kind, func(t *testing.T) {
			db := openRecoveryWorkerMySQL(t)
			value, event, service, store, project := recoveryWorkerFixture(t, db)
			worker := NewWorker(recoveryCrashStore{store}, service, project, Config{WorkerID: "worker", LeaseDuration: time.Minute, BatchSize: 1})
			if n, err := worker.RunOnce(context.Background()); n != 1 || err == nil {
				t.Fatalf("crash: n=%d err=%v", n, err)
			}
			applyRecoveryCallback(t, service, value, project.ids[event.EventID])
			worker.store = store
			worker.now = func() time.Time { return time.Now().Add(2 * time.Minute).UTC() }
			if kind == "exhausted_outage" {
				project.err = errors.New("remote offline after acceptance")
				db.Model(&event).Update("retry_count", 6)
			}
			if kind == "wrong_binding" {
				project.conflicting = true
			}
			n, err := worker.RunOnce(context.Background())
			if n != 1 || (kind == "replay" && err != nil) || (kind != "replay" && err == nil) {
				t.Fatalf("replay: n=%d err=%v", n, err)
			}
			var current report.Request
			db.First(&current, value.ID)
			var stored report.Outbox
			db.First(&stored, event.ID)
			var notices, events int64
			db.Model(&report.Notification{}).Count(&notices)
			db.Model(&report.StatusEvent{}).Count(&events)
			wantStatus := report.OutboxSent
			if kind == "wrong_binding" {
				wantStatus = report.OutboxRetryWait
			}
			if current.Status != report.StatusApprovedProcessing || current.DownstreamRequestID != "external-delivery-event" || stored.Status != wantStatus || notices != 0 || events != 2 || len(project.ids) != 1 {
				t.Fatalf("after crash: request=%s downstream=%s outbox=%s notices=%d events=%d", current.Status, current.DownstreamRequestID, stored.Status, notices, events)
			}
		})
	}
}

// TestWorkerDeadLetterAtomicRollbackOnMySQL 验证Outbox终止写失败不泄漏领域失败投影。
// 参数：t为测试上下文。
// 返回值：无；申请、状态事件或通知发生部分提交时失败。
func TestWorkerDeadLetterAtomicRollbackOnMySQL(t *testing.T) {
	db := openRecoveryWorkerMySQL(t)
	value, event, service, store, project := recoveryWorkerFixture(t, db)
	db.Model(&event).Update("retry_count", 6)
	project.err = errors.New("external offline")
	store.updateOutbox = func(context.Context, report.Outbox, string, map[string]any) error {
		return errors.New("injected outbox write failure")
	}
	worker := NewWorker(store, service, project, Config{WorkerID: "worker", LeaseDuration: time.Minute, BatchSize: 1})
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("outbox failure accepted")
	}
	var current report.Request
	db.First(&current, value.ID)
	var notices, events int64
	db.Model(&report.Notification{}).Count(&notices)
	db.Model(&report.StatusEvent{}).Count(&events)
	if current.Status != report.StatusSubmitted || notices != 0 || events != 0 {
		t.Fatalf("partial commit: %s notices=%d events=%d", current.Status, notices, events)
	}
}
