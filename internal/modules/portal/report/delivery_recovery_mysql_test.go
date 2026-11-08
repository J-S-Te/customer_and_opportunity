package report

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// seedDeliveryRecovery 创建实际死信投影及原始投递身份。
// 参数：t为测试上下文，db为隔离连接。
// 返回值：已失败申请和待管理员恢复的事件。
func seedDeliveryRecovery(t *testing.T, db *gorm.DB) (Request, Outbox) {
	t.Helper()
	value := seedRevisionRequest(t, db, 73, StatusSubmitted)
	value.DownstreamRequestID = ""
	if err := db.Model(&value).Update("downstream_request_id", "").Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"request_id": value.ID, "request_no": value.RequestNo, "customer_id": value.CustomerID, "project_id": value.ProjectID, "report_type": value.ReportType, "reason": value.Reason})
	event := Outbox{EventID: "recovery-event", TenantID: value.TenantID, EventType: OutboxEventSubmitted, AggregateID: value.ID, Payload: payload, Status: OutboxProcessing, LockedBy: "worker", RetryCount: 6, CreatedAt: time.Now().UTC()}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewGORMRepository(db), nil, nil, nil, reportClock{now: time.Now()}, nil)
	if err := service.MarkDeliveryDeadLetter(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&event).Updates(map[string]any{"status": OutboxDeadLetter, "locked_by": "", "retry_count": 7}).Error; err != nil {
		t.Fatal(err)
	}
	return value, event
}

// TestDeliveryRetryRestoresRequestOnMySQL 验证并发管理员恢复只推进一次且后续受理回调可用。
// 参数：t为测试上下文。
// 返回值：无；并发重试、身份或事件通知不符合约定时失败。
func TestDeliveryRetryRestoresRequestOnMySQL(t *testing.T) {
	db := openRevisionNotificationMySQL(t, true)
	value, event := seedDeliveryRecovery(t, db)
	service := NewService(NewGORMRepository(db), nil, nil, nil, reportClock{now: time.Now()}, nil)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- service.RetryDeliveryFailure(context.Background(), value.TenantID, event.EventID)
		}()
	}
	wg.Wait()
	close(results)
	applied := 0
	for err := range results {
		if err == nil {
			applied++
		} else if !errors.Is(err, ErrDeliveryFailureNotRetryable) {
			t.Fatalf("concurrent retry: %v", err)
		}
	}
	if applied != 1 {
		t.Fatalf("retry applied %d times", applied)
	}
	var current Request
	db.First(&current, value.ID)
	if current.Status != StatusSubmitted || current.DownstreamRequestID != "" {
		t.Fatalf("retry did not restore request: %s %s", current.Status, current.DownstreamRequestID)
	}
	if err := service.MarkApprovalStarted(context.Background(), value.TenantID, value.ID, "external-recovered"); err != nil {
		t.Fatal(err)
	}
	cb := Callback{TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID, ProjectID: value.ProjectID, DownstreamRequestID: "external-recovered", Version: 1, IdempotencyKey: "recovered-callback", Status: StatusApprovedProcessing, ApprovalResult: "APPROVED"}
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	db.First(&current, value.ID)
	if current.Status != StatusApprovedProcessing {
		t.Fatalf("callback status: %s", current.Status)
	}
	var notices, events int64
	db.Model(&Notification{}).Count(&notices)
	db.Model(&StatusEvent{}).Count(&events)
	if notices != 1 || events != 4 {
		t.Fatalf("duplicate/missing history: notices=%d events=%d", notices, events)
	}
}

// TestDeliveryRetryRejectsMismatchedBindingOnMySQL 验证错租户、聚合或持久化载荷不能恢复申请。
// 参数：t为测试上下文。
// 返回值：无；错误身份重试成功或领域状态改变时失败。
func TestDeliveryRetryRejectsMismatchedBindingOnMySQL(t *testing.T) {
	for _, kind := range []string{"tenant", "aggregate", "payload", "event_type"} {
		t.Run(kind, func(t *testing.T) {
			db := openRevisionNotificationMySQL(t, true)
			value, event := seedDeliveryRecovery(t, db)
			tenant := value.TenantID
			switch kind {
			case "tenant":
				tenant = "other"
			case "aggregate":
				db.Model(&event).Update("aggregate_id", 999)
			case "payload":
				db.Model(&event).Update("payload", `{"request_id":73,"customer_id":100,"project_id":"other"}`)
			case "event_type":
				db.Model(&event).Update("event_type", "OTHER")
			}
			service := NewService(NewGORMRepository(db), nil, nil, nil, reportClock{now: time.Now()}, nil)
			if err := service.RetryDeliveryFailure(context.Background(), tenant, event.EventID); err == nil {
				t.Fatal("mismatched event accepted")
			}
			var current Request
			db.First(&current, value.ID)
			var stored Outbox
			db.First(&stored, event.ID)
			if current.Status != StatusSubmissionFailed || stored.Status != OutboxDeadLetter {
				t.Fatalf("mismatch changed state: %s %s", current.Status, stored.Status)
			}
		})
	}
}

// TestDeliveryRetryRollsBackOnMySQL 验证恢复事件写入失败时申请与死信一起回滚。
// 参数：t为测试上下文。
// 返回值：无；事务部分成功时失败。
func TestDeliveryRetryRollsBackOnMySQL(t *testing.T) {
	db := openRevisionNotificationMySQL(t, true)
	value, event := seedDeliveryRecovery(t, db)
	if err := db.Exec(`CREATE TRIGGER fail_delivery_retry BEFORE INSERT ON portal_report_status_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected history failure'`).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewGORMRepository(db), nil, nil, nil, reportClock{now: time.Now()}, nil)
	if err := service.RetryDeliveryFailure(context.Background(), value.TenantID, event.EventID); err == nil {
		t.Fatal("history failure accepted")
	}
	var current Request
	db.First(&current, value.ID)
	var stored Outbox
	db.First(&stored, event.ID)
	if current.Status != StatusSubmissionFailed || stored.Status != OutboxDeadLetter {
		t.Fatalf("partial commit: %s %s", current.Status, stored.Status)
	}
}

// TestDeliveryRepeatedRetryFailureOnMySQL 验证管理员恢复后再次耗尽仍能收口且不重复通知。
// 参数：t为测试上下文。
// 返回值：无；第二次失败不能提交或重复通知时失败。
func TestDeliveryRepeatedRetryFailureOnMySQL(t *testing.T) {
	db := openRevisionNotificationMySQL(t, true)
	value, event := seedDeliveryRecovery(t, db)
	service := NewService(NewGORMRepository(db), nil, nil, nil, reportClock{now: time.Now()}, nil)
	if err := service.RetryDeliveryFailure(context.Background(), value.TenantID, event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&event).Updates(map[string]any{"status": OutboxProcessing, "locked_by": "worker"}).Error; err != nil {
		t.Fatal(err)
	}
	event.Status, event.LockedBy = OutboxProcessing, "worker"
	if err := service.MarkDeliveryDeadLetter(context.Background(), event); err != nil {
		t.Fatalf("second exhaustion: %v", err)
	}
	var current Request
	db.First(&current, value.ID)
	var notices, events int64
	db.Model(&Notification{}).Count(&notices)
	db.Model(&StatusEvent{}).Count(&events)
	if current.Status != StatusSubmissionFailed || notices != 1 || events != 3 {
		t.Fatalf("second failure: %s notices=%d events=%d", current.Status, notices, events)
	}
}
