package report

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

// ErrDeliveryAlreadyAccepted 告知投递层已有可信绑定，应收口SENT而非写假失败。
var ErrDeliveryAlreadyAccepted = errors.New("report delivery was already accepted")

// deliveryAccepted 判断状态是否能由已经绑定的下游受理流程合法产生。
// 参数：status为申请当前状态。
// 返回值：允许幂等确认返回真，提交前或未知状态返回假。
func deliveryAccepted(status Status) bool {
	switch status {
	case StatusApproving, StatusApprovedProcessing, StatusIngestPending,
		StatusIssued, StatusRejected, StatusProcessingFailed:
		return true
	default:
		return false
	}
}

// deliveryPayloadMatches 核对原始报文与聚合业务身份，禁止借错绑事件恢复其他申请。
// 参数：event为持久化原始事件，value为同租户行锁内申请。
// 返回值：全部业务绑定一致时返回真；损坏或不一致返回假。
func deliveryPayloadMatches(event Outbox, value *Request) bool {
	var payload struct {
		RequestID  uint64 `json:"request_id"`
		RequestNo  string `json:"request_no"`
		CustomerID uint64 `json:"customer_id"`
		ProjectID  string `json:"project_id"`
		ReportType string `json:"report_type"`
		Reason     string `json:"reason"`
	}
	return json.Unmarshal(event.Payload, &payload) == nil && event.TenantID == value.TenantID &&
		event.AggregateID == value.ID && payload.RequestID == value.ID && payload.RequestNo == value.RequestNo &&
		payload.CustomerID == value.CustomerID && payload.ProjectID == value.ProjectID &&
		payload.ReportType == value.ReportType && payload.Reason == value.Reason
}

// restoreDeliveryRequest 恢复已验证事件产生的提交失败，后续状态只允许保留原进度。
// 参数：ctx为同时锁定事件和申请的事务，value为申请，event为已核对的死信。
// 返回值：无；无合法失败来源或状态不兼容时拒绝，写入失败时整体回滚。
func (r *GORMRepository) restoreDeliveryRequest(ctx context.Context, value *Request, event Outbox) error {
	if value.Status == StatusSubmitted && value.DownstreamRequestID == "" {
		return nil
	}
	if deliveryAccepted(value.Status) && value.DownstreamRequestID != "" {
		return nil
	}
	if value.Status != StatusSubmissionFailed || value.DownstreamRequestID != "" {
		return ErrInvalidTransition
	}
	failure, err := r.FindStatusEventBySource(
		ctx, event.TenantID, value.ID, sourceHash("DELIVERY_DEAD_LETTER", event.EventID),
	)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidTransition
	}
	if err != nil {
		return err
	}
	if failure.EventType != "SUBMISSION_FAILED" || failure.ToStatus != StatusSubmissionFailed ||
		failure.CustomerID != value.CustomerID {
		return ErrInvalidTransition
	}
	now := time.Now().UTC()
	if err = r.Update(ctx, value, value.Version, map[string]any{
		"status": StatusSubmitted, "updated_by": "portal-report-admin", "updated_at": now,
	}); err != nil {
		return err
	}
	key := event.EventID + ":" + strconv.FormatUint(value.Version, 10)
	eventRecord := statusEvent(
		value, "SUBMISSION_RETRIED", value.Version+1, StatusSubmissionFailed, StatusSubmitted,
		"SYSTEM", "portal-report-admin", "DELIVERY_RETRY", key, "", requestctx.ID(ctx), now,
	)
	return r.CreateStatusEvent(ctx, eventRecord)
}

// recordDeliveryFailure 逐次保存失败历史，同时保留首次失败通知的唯一身份。
// 参数：ctx为投递事务，value为失败前申请，eventID为原始投递事件，now为失败时间。
// 返回值：无；历史或通知保存失败时回滚整个投递事务。
func (s *Service) recordDeliveryFailure(ctx context.Context, value *Request, eventID string, now time.Time) error {
	_, err := s.repo.FindStatusEventBySource(ctx, value.TenantID, value.ID, sourceHash("DELIVERY_DEAD_LETTER", eventID))
	first := errors.Is(err, ErrNotFound)
	if err != nil && !first {
		return err
	}
	key := eventID
	if !first {
		key += ":" + strconv.FormatUint(value.Version, 10)
	}
	event := statusEvent(
		value, "SUBMISSION_FAILED", value.Version+1, StatusSubmitted, StatusSubmissionFailed,
		"SYSTEM", "portal-report-worker", "DELIVERY_DEAD_LETTER", key, "", requestctx.ID(ctx), now,
	)
	if err = s.repo.CreateStatusEvent(ctx, event); err != nil {
		return err
	}
	if !first {
		return nil
	}
	return s.repo.CreateNotification(ctx, &Notification{
		TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID,
		AccountID: value.AccountID, Kind: NotificationKindSubmissionFailed,
		Status: NotificationUnread, CreatedAt: now,
	})
}
