package report

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件只承载投递死信的运维面（列表/重试）与死信租约复核所需的最小仓储访问。
// 恢复必须复核持久化事件与申请身份，并在同一事务内更新申请和投递状态。

var (
	// ErrDeliveryFailureNotFound 对跨租户或不存在的事件保持同一响应，避免枚举其他租户的死信。
	ErrDeliveryFailureNotFound = apperror.New(http.StatusNotFound, "PORTAL_REPORT_DELIVERY_FAILURE_NOT_FOUND", "report delivery failure not found")
	// ErrDeliveryFailureNotRetryable 表示该行已不在死信态（例如已被其他运维会话重试），不能重复推进。
	ErrDeliveryFailureNotRetryable = apperror.New(http.StatusConflict, "PORTAL_REPORT_DELIVERY_FAILURE_NOT_RETRYABLE", "report delivery failure is not retryable")
)

// OutboxFailureView 是运维死信列表的最小投影：不含下行载荷、事件类型与租户身份。
type OutboxFailureView struct {
	EventID          string `json:"event_id"`
	RequestID        uint64 `json:"request_id"`
	RetryCount       uint8  `json:"retry_count"`
	LastErrorSummary string `json:"last_error_summary"`
}

// OutboxRetryResult 区分“不存在/跨租户”“已重置为可重试”“不可重试”三种结果。
type OutboxRetryResult int

const (
	OutboxRetryNotFound OutboxRetryResult = iota
	OutboxRetryApplied
	OutboxRetryNotRetryable
)

// deliveryLeaseRepository 持有投递 Outbox 行的行锁并以稳定身份复核租约。
// 未实现该能力的仓储一律失败关闭，不能在没有租约证据的情况下写申请终态。
type deliveryLeaseRepository interface {
	FindOutboxForUpdate(context.Context, uint64) (*Outbox, error)
}

// deliveryAdminRepository 是运维死信列表与重试的最小读写面，始终按验签主体的租户过滤。
type deliveryAdminRepository interface {
	ListOutboxDeadLetters(context.Context, string, int, int) (pagination.Page[OutboxFailureView], error)
	RetryOutboxDeadLetter(context.Context, string, string) (OutboxRetryResult, error)
}

// ListDeliveryFailures 只返回当前租户处于死信态的投递事件，按最新优先分页。
func (s *Service) ListDeliveryFailures(ctx context.Context, tenantID string, page, pageSize int) (pagination.Page[OutboxFailureView], error) {
	tenantID = strings.TrimSpace(tenantID)
	empty := pagination.Page[OutboxFailureView]{Items: []OutboxFailureView{}}
	if !validBoundedText(tenantID, maxTenantIDBytes) {
		return empty, ErrDeliveryFailureNotFound
	}
	store, ok := s.repo.(deliveryAdminRepository)
	if !ok {
		return empty, apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_DELIVERY_ADMIN_UNAVAILABLE", "report delivery administration is unavailable")
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	value, err := store.ListOutboxDeadLetters(ctx, tenantID, page, pageSize)
	if err != nil {
		return empty, err
	}
	if value.Items == nil {
		value.Items = []OutboxFailureView{}
	}
	return value, nil
}

// RetryDeliveryFailure 恢复当前租户死信及可恢复申请，保留原有下游幂等身份。
// 参数：ctx为管理请求上下文，tenantID为已认证租户，eventID为原投递事件。
// 返回值：无；跨租户或不存在返回未找到，已恢复或身份不一致返回冲突。
func (s *Service) RetryDeliveryFailure(ctx context.Context, tenantID, eventID string) error {
	tenantID, eventID = strings.TrimSpace(tenantID), strings.TrimSpace(eventID)
	if !validBoundedText(tenantID, maxTenantIDBytes) || !validBoundedText(eventID, 64) {
		return ErrDeliveryFailureNotFound
	}
	store, ok := s.repo.(deliveryAdminRepository)
	if !ok {
		return apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_DELIVERY_ADMIN_UNAVAILABLE", "report delivery administration is unavailable")
	}
	result, err := store.RetryOutboxDeadLetter(ctx, tenantID, eventID)
	if err != nil {
		return err
	}
	switch result {
	case OutboxRetryApplied:
		return nil
	case OutboxRetryNotRetryable:
		return ErrDeliveryFailureNotRetryable
	default:
		return ErrDeliveryFailureNotFound
	}
}

// FindOutboxForUpdate 用行锁读取投递事件，供死信标记在调用方事务内复核状态与租约归属。
func (r *GORMRepository) FindOutboxForUpdate(ctx context.Context, id uint64) (*Outbox, error) {
	var value Outbox
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &value, err
}

// ListOutboxDeadLetters 按租户读取死信分页，只选出脱敏后的最小列。
func (r *GORMRepository) ListOutboxDeadLetters(ctx context.Context, tenantID string, pageNo, pageSize int) (pagination.Page[OutboxFailureView], error) {
	page := pagination.Page[OutboxFailureView]{Items: []OutboxFailureView{}, Page: pageNo, PageSize: pageSize}
	base := r.tx(ctx).Model(&Outbox{}).
		Where("tenant_id=? AND event_type=? AND status=?", tenantID, OutboxEventSubmitted, OutboxDeadLetter)
	if err := base.Count(&page.Total).Error; err != nil {
		return page, err
	}
	err := base.Select("event_id, aggregate_id AS request_id, retry_count, last_error_summary").
		Order("id DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&page.Items).Error
	return page, err
}

// RetryOutboxDeadLetter 在行锁事务内恢复同租户申请和原投递事件。
// 参数：ctx为管理上下文，tenantID为已认证租户，eventID为原死信事件。
// 返回值：恢复结果；身份冲突、状态冲突或存储错误不产生部分更新。
func (r *GORMRepository) RetryOutboxDeadLetter(ctx context.Context, tenantID, eventID string) (OutboxRetryResult, error) {
	result := OutboxRetryNotFound
	err := r.WithTransaction(ctx, func(tx context.Context) error {
		var event Outbox
		err := r.tx(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id=? AND event_id=?", tenantID, eventID).Take(&event).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		result = OutboxRetryNotRetryable
		if event.EventType != OutboxEventSubmitted || event.Status != OutboxDeadLetter {
			return nil
		}
		value, err := r.FindForUpdate(tx, tenantID, event.AggregateID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !deliveryPayloadMatches(event, value) {
			return nil
		}
		if err = r.restoreDeliveryRequest(tx, value, event); errors.Is(err, ErrInvalidTransition) {
			return nil
		}
		if err != nil {
			return err
		}
		update := r.tx(tx).Model(&Outbox{}).
			Where("id=? AND tenant_id=? AND event_id=? AND status=?",
				event.ID, tenantID, eventID, OutboxDeadLetter).
			Updates(map[string]any{
				"status": OutboxRetryWait, "retry_count": 0, "next_retry_at": nil,
				"locked_by": "", "locked_until": nil,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDeliveryFailureNotRetryable
		}
		result = OutboxRetryApplied
		return nil
	})
	return result, err
}
