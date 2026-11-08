package report

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDeliveryReplayPreservesAdvancedRequest 验证回调先到后相同绑定可重投且不回退。
// 参数：t为测试上下文。
// 返回值：无；重投失败、错绑成功或重复写入时失败。
func TestDeliveryReplayPreservesAdvancedRequest(t *testing.T) {
	for _, status := range []Status{StatusApprovedProcessing, StatusIngestPending, StatusIssued, StatusRejected, StatusProcessingFailed} {
		t.Run(string(status), func(t *testing.T) {
			value := callbackRequest()
			value.Status = status
			repo := &reportRepoStub{request: value}
			service := NewService(repo, nil, nil, nil, reportClock{now: time.Now()}, nil)
			if err := service.MarkApprovalStarted(context.Background(), value.TenantID, value.ID, value.DownstreamRequestID); err != nil {
				t.Fatalf("same binding replay: %v", err)
			}
			if err := service.MarkApprovalStarted(context.Background(), value.TenantID, value.ID, "different-id"); !errors.Is(err, ErrCallbackConflict) {
				t.Fatalf("wrong binding: %v", err)
			}
			if repo.updateRuns != 0 || len(repo.events) != 0 || len(repo.notifications) != 0 {
				t.Fatal("replay changed progressed request")
			}
		})
	}
}

// TestDeliveryDeadLetterPreservesAcceptedRequest 验证已受理申请不被投递耗尽误标失败。
// 参数：t为测试上下文。
// 返回值：无；未识别已绑定申请或写入假失败时失败。
func TestDeliveryDeadLetterPreservesAcceptedRequest(t *testing.T) {
	for _, status := range []Status{StatusApproving, StatusApprovedProcessing, StatusIngestPending, StatusIssued, StatusRejected, StatusProcessingFailed} {
		t.Run(string(status), func(t *testing.T) {
			repo, event := deliveryDeadLetterFixture(status)
			service := NewService(repo, nil, nil, nil, reportClock{now: time.Now()}, nil)
			// 已受理信号由Worker转为SENT，不能追加失败事件或通知。
			err := service.MarkDeliveryDeadLetter(context.Background(), event)
			if !errors.Is(err, ErrDeliveryAlreadyAccepted) {
				t.Fatalf("accepted delivery must be distinguishable: %v", err)
			}
			if repo.updateRuns != 0 || len(repo.events) != 0 || len(repo.notifications) != 0 {
				t.Fatal("accepted request falsely failed")
			}
		})
	}
}
