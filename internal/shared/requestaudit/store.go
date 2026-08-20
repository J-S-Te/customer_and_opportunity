package requestaudit

import (
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	crmTableName    = "application_request_audit_outbox"
	portalTableName = "portal_application_request_audit_outbox"
)

type Store struct {
	db        *gorm.DB
	tableName string
}

func NewStore(db *gorm.DB) *Store { return &Store{db: db, tableName: crmTableName} }

func NewPortalStore(db *gorm.DB) *Store { return &Store{db: db, tableName: portalTableName} }

func (s *Store) Publish(ctx context.Context, value BusinessEvent) error {
	now := value.OccurredAt.UTC()
	requestID := value.RequestID
	if requestID == "" {
		requestID = value.EventID
	}
	return database.FromContext(ctx, s.db).Table(s.tableName).Create(&Record{
		EventID: value.EventID, TenantID: value.TenantID, ApplicationCode: value.ApplicationCode,
		EnvironmentCode: value.EnvironmentCode, ActorType: value.ActorType, ActorID: value.ActorID,
		ActorName: value.ActorName, UserLoginIP: value.UserLoginIP, Action: value.Action, ResourceType: value.ResourceType,
		ResourceID: value.ResourceID, RequestID: requestID, Method: "BUSINESS", Route: "BUSINESS",
		Result: value.Result, ReasonCode: value.ReasonCode, RiskLevel: value.RiskLevel,
		DeliveryStatus: StatusPending, NextAttemptAt: &now, OccurredAt: now, CompletedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error
}

func (s *Store) Start(ctx context.Context, value Start) error {
	now := value.OccurredAt.UTC()
	return s.db.WithContext(ctx).Table(s.tableName).Create(&Record{
		EventID: value.EventID, TenantID: value.TenantID, ApplicationCode: value.ApplicationCode,
		EnvironmentCode: value.EnvironmentCode, ActorType: "SYSTEM", Action: "HTTP_REQUEST_STARTED",
		ResourceType: "http_route", ResourceID: "", RequestID: value.RequestID, Method: value.Method, Route: "UNMATCHED",
		Result: "FAILURE", RiskLevel: "LOW", DeliveryStatus: StatusStarted, OccurredAt: now,
		CreatedAt: now, UpdatedAt: now,
	}).Error
}

func (s *Store) Complete(ctx context.Context, eventID string, value Completion) error {
	now := value.CompletedAt.UTC()
	result := s.db.WithContext(ctx).Table(s.tableName).
		Where("event_id=? AND delivery_status=?", eventID, StatusStarted).
		Updates(map[string]any{
			"actor_type": value.ActorType, "actor_id": value.ActorID, "actor_name": value.ActorName, "user_login_ip": value.UserLoginIP,
			"action": value.Action, "route": value.Route, "http_status": value.HTTPStatus,
			"result": value.Result, "reason_code": value.ReasonCode, "risk_level": value.RiskLevel,
			"delivery_status": StatusPending, "next_attempt_at": now, "completed_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("request audit reservation was not finalized")
	}
	return nil
}

// RecoverInterrupted converts abandoned reservations into explicit failures.
// A process crash therefore still produces an auditable terminal fact instead
// of silently dropping the operation that had already been admitted.
func (s *Store) RecoverInterrupted(ctx context.Context, staleBefore, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(s.tableName).
			Where("delivery_status=? AND created_at<?", StatusStarted, staleBefore.UTC()).
			Updates(map[string]any{
				"action": "HTTP_REQUEST_INTERRUPTED", "result": "FAILURE", "reason_code": "PROCESS_INTERRUPTED",
				"risk_level": "HIGH", "http_status": 500, "delivery_status": StatusPending,
				"next_attempt_at": now.UTC(), "completed_at": now.UTC(), "updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		// A process may die after claiming a batch or while acknowledging its
		// receipts. Once the lease expires, make the same event IDs retryable;
		// platform-side deduplication makes an already accepted event safe.
		return tx.Table(s.tableName).
			Where("delivery_status=? AND locked_until<=?", StatusProcessing, now.UTC()).
			Updates(map[string]any{
				"delivery_status": StatusRetry, "next_attempt_at": now.UTC(), "locked_by": "",
				"locked_until": nil, "last_error_code": "AUDIT_DELIVERY_INTERRUPTED", "updated_at": now.UTC(),
			}).Error
	})
}

func (s *Store) Claim(ctx context.Context, workerID string, limit int, now, lockedUntil time.Time) ([]Record, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var values []Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(s.tableName).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("delivery_status IN (?,?) AND next_attempt_at<=? AND (locked_until IS NULL OR locked_until<=?)", StatusPending, StatusRetry, now.UTC(), now.UTC()).
			Order("id").Limit(limit).Find(&values).Error; err != nil {
			return err
		}
		if len(values) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(values))
		for _, value := range values {
			ids = append(ids, value.ID)
		}
		return tx.Table(s.tableName).Where("id IN ?", ids).Updates(map[string]any{
			"delivery_status": StatusProcessing, "locked_by": workerID, "locked_until": lockedUntil.UTC(), "updated_at": now.UTC(),
		}).Error
	})
	return values, err
}

func (s *Store) Delivered(ctx context.Context, workerID string, values []Record, now time.Time) error {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	return s.db.WithContext(ctx).Table(s.tableName).
		Where("id IN ? AND delivery_status=? AND locked_by=?", ids, StatusProcessing, workerID).
		Updates(map[string]any{
			"delivery_status": StatusDelivered, "delivered_at": now.UTC(), "locked_by": "", "locked_until": nil,
			"last_error_code": "", "updated_at": now.UTC(),
		}).Error
}

func (s *Store) Retry(ctx context.Context, workerID string, values []Record, code string, next, now time.Time) error {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	return s.db.WithContext(ctx).Table(s.tableName).
		Where("id IN ? AND delivery_status=? AND locked_by=?", ids, StatusProcessing, workerID).
		Updates(map[string]any{
			"delivery_status": StatusRetry, "attempts": gorm.Expr("attempts+1"), "next_attempt_at": next.UTC(),
			"locked_by": "", "locked_until": nil, "last_error_code": code, "updated_at": now.UTC(),
		}).Error
}

// Status returns a tenant-scoped, aggregate-only view for operational
// monitoring. It never returns audit payloads, tokens, or request content.
func (s *Store) Status(ctx context.Context, tenantID string) (OutboxStatus, error) {
	status := OutboxStatus{ErrorCounts: map[string]int64{}}
	query := func() *gorm.DB {
		return s.db.WithContext(ctx).Table(s.tableName).Where("tenant_id = ?", tenantID)
	}

	type countRow struct {
		DeliveryStatus string
		Count          int64
	}
	var counts []countRow
	if err := query().Select("delivery_status, COUNT(*) AS count").Group("delivery_status").Scan(&counts).Error; err != nil {
		return OutboxStatus{}, err
	}
	for _, row := range counts {
		switch row.DeliveryStatus {
		case StatusPending:
			status.PendingCount = row.Count
		case StatusRetry:
			status.RetryCount = row.Count
		case StatusProcessing:
			status.ProcessingCount = row.Count
		case StatusStarted:
			status.StartedCount = row.Count
		case StatusDelivered:
			status.DeliveredCount = row.Count
		}
	}

	type aggregateRow struct {
		OldestUndeliveredAt *time.Time
		MaxAttempts         uint32
		LastDeliveredAt     *time.Time
	}
	var aggregate aggregateRow
	if err := query().Select("MIN(CASE WHEN delivery_status IN (?, ?, ?, ?) THEN occurred_at END) AS oldest_undelivered_at, MAX(attempts) AS max_attempts, MAX(delivered_at) AS last_delivered_at", StatusStarted, StatusPending, StatusProcessing, StatusRetry).Scan(&aggregate).Error; err != nil {
		return OutboxStatus{}, err
	}
	status.OldestUndeliveredAt = aggregate.OldestUndeliveredAt
	status.MaxAttempts = aggregate.MaxAttempts
	status.LastDeliveredAt = aggregate.LastDeliveredAt

	type errorCountRow struct {
		Code  string
		Count int64
	}
	var errors []errorCountRow
	if err := query().Select("last_error_code AS code, COUNT(*) AS count").Where("last_error_code <> ''").Group("last_error_code").Scan(&errors).Error; err != nil {
		return OutboxStatus{}, err
	}
	for _, row := range errors {
		status.ErrorCounts[row.Code] = row.Count
	}
	return status, nil
}
