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
// 申请状态机仍由 Service 的领域方法维护，运维面不能直接改写申请状态。

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

// RetryDeliveryFailure 只把当前租户的死信事件重置为可重试（RETRY_WAIT + retry_count=0）。
// 不修改载荷、事件身份或申请状态：客户侧的恢复方式仍是新建申请并使用新幂等键。
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

// RetryOutboxDeadLetter 只在当前租户、当前事件仍处死信态时原子重置退避计数。
func (r *GORMRepository) RetryOutboxDeadLetter(ctx context.Context, tenantID, eventID string) (OutboxRetryResult, error) {
	result := r.tx(ctx).Model(&Outbox{}).
		Where("tenant_id=? AND event_id=? AND event_type=? AND status=?", tenantID, eventID, OutboxEventSubmitted, OutboxDeadLetter).
		Updates(map[string]any{
			"status": OutboxRetryWait, "retry_count": 0, "next_retry_at": nil,
			"locked_by": "", "locked_until": nil,
		})
	if result.Error != nil {
		return OutboxRetryNotFound, result.Error
	}
	if result.RowsAffected == 1 {
		return OutboxRetryApplied, nil
	}
	var count int64
	if err := r.tx(ctx).Model(&Outbox{}).Where("tenant_id=? AND event_id=?", tenantID, eventID).Count(&count).Error; err != nil {
		return OutboxRetryNotFound, err
	}
	if count == 0 {
		return OutboxRetryNotFound, nil
	}
	return OutboxRetryNotRetryable, nil
}
