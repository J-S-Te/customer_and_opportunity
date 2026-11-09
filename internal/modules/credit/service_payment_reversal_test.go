package credit

import (
	"testing"
	"time"
)

// TestReversalIdentityMatchesPersistedMoneyScale 验证数据库金额精度规范化不改变付款身份。
// 参数：t为测试上下文。
// 返回值：相同数值不同小数位被拒绝或不同金额被接受时失败。
func TestReversalIdentityMatchesPersistedMoneyScale(t *testing.T) {
	due := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	original := creditPaymentRecord{EventType: PaymentEventCompleted, CustomerID: 42, DueDate: &due, DueAmount: "100.00", SourceSystem: "settlement"}
	event := PaymentEvent{CustomerID: 42, DueDate: due, DueAmount: "100", SourceSystem: "settlement"}
	for _, value := range []string{"100", "100.0", "100.00"} {
		event.DueAmount = value
		if !sameReversalTarget(original, event) {
			t.Errorf("same persisted amount rejected: %s", value)
		}
	}
	for _, value := range []string{"101", "garbage", ""} {
		event.DueAmount = value
		if sameReversalTarget(original, event) {
			t.Errorf("different/invalid amount accepted: %s", value)
		}
	}
	original.EventType = ""
	event.DueAmount = "100.00"
	if sameReversalTarget(original, event) {
		t.Fatal("unknown historical event type must not be guessed")
	}
}
