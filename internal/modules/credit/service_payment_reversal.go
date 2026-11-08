package credit

import (
	"github.com/shopspring/decimal"
	"time"
)

// sameReversalTarget 校验补偿事件与原付款的不可变业务身份一致。
// 参数：original为锁内读取的原事件，event为待处理补偿事件。
// 返回值：客户、来源、账期、金额及业务日期全部一致时返回true。
func sameReversalTarget(original creditPaymentRecord, event PaymentEvent) bool {
	if original.EventType != PaymentEventCompleted || original.CustomerID != event.CustomerID ||
		original.SourceSystem != event.SourceSystem || original.ContractNo != event.ContractNo ||
		original.PeriodNo != event.PeriodNo ||
		original.DueDate == nil || !original.DueDate.Equal(event.DueDate) {
		return false
	}
	originalAmount, originalErr := decimal.NewFromString(original.DueAmount)
	eventAmount, eventErr := decimal.NewFromString(event.DueAmount)
	return originalErr == nil && eventErr == nil && originalAmount.Equal(eventAmount) &&
		samePaymentDate(original.PaidDate, event.PaidDate)
}

// samePaymentDate 比较付款日期并保留未付款的空值语义。
// 参数：left和right为原事件及补偿事件的付款日期。
// 返回值：日期同为空或表示相同时刻时返回true。
func samePaymentDate(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// paymentHadNoCreditEffect 识别正式领域写入且从未改变信用的忽略结果。
// 参数：evaluation为原付款持久化的评价状态。
// 返回值：已知无效果状态返回true，其余状态拒绝无效果补偿。
func paymentHadNoCreditEffect(evaluation string) bool {
	switch evaluation {
	case "IGNORED_CUSTOMER_UNAVAILABLE", "IGNORED_OUT_OF_ORDER", "IGNORED_RULE_DISABLED", "IGNORED_INCOMPLETE":
		return true
	default:
		return false
	}
}
