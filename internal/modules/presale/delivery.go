package presale

import (
	"context"
	"fmt"
	"strings"
	"time"
)

var retryDelays = [...]time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	time.Hour,
	3 * time.Hour,
	6 * time.Hour,
}

// MarkDeliverySending is called by the outbox worker after it atomically claims
// an event. It only updates local delivery projection; it does not call PMS.
func (s *Service) MarkDeliverySending(ctx context.Context, tenant string, worklogID uint64) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		worklog, err := s.repo.FindWorklogForUpdate(tx, tenant, worklogID)
		if err != nil {
			return err
		}
		// A lease may expire after the worker has changed the projection but
		// before it has acknowledged the outbox event. Reclaiming that event is
		// therefore allowed to resume from SENDING.
		if worklog.PushStatus == PushSending || worklog.PushStatus == PushSuccess {
			return nil
		}
		if worklog.PushStatus != PushPending && worklog.PushStatus != PushRetryWait {
			return ErrInvalidTransition
		}
		return s.repo.UpdateWorklogDelivery(tx, tenant, worklogID, map[string]any{"push_status": PushSending, "updated_at": s.clock.Now()})
	})
}

// DeliveryRetryAt is the single retry policy shared by the domain projection
// and outbox worker. The attempt number is one-based.
func DeliveryRetryAt(now time.Time, attempt uint8) (*time.Time, bool) {
	if attempt == 0 || int(attempt) > len(retryDelays) {
		return nil, false
	}
	next := now.Add(retryDelays[attempt-1])
	return &next, true
}

// MarkDeliverySuccess records a successful real PMS response. Repeated success
// acknowledgements are idempotent.
func (s *Service) MarkDeliverySuccess(ctx context.Context, tenant string, worklogID uint64, responseCode string) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		worklog, err := s.repo.FindWorklogForUpdate(tx, tenant, worklogID)
		if err != nil {
			return err
		}
		if worklog.PushStatus == PushSuccess {
			return nil
		}
		if worklog.PushStatus != PushSending {
			return ErrInvalidTransition
		}
		attempt := worklog.PushAttempts + 1
		if err = s.repo.UpdateWorklogDelivery(tx, tenant, worklogID, map[string]any{"push_status": PushSuccess, "push_attempts": attempt, "next_retry_at": nil, "last_error_summary": "", "updated_at": s.clock.Now()}); err != nil {
			return err
		}
		return s.repo.CreateIntegrationAttempt(tx, &IntegrationAttempt{TenantID: tenant, WorklogID: worklogID, AttemptNo: attempt, Result: "SUCCESS", ResponseCode: truncate(responseCode, 32), AttemptedAt: s.clock.Now()})
	})
}

// MarkDeliveryFailure records a sanitized failure and computes deterministic
// retry timing. Infrastructure may add bounded jitter before scheduling.
func (s *Service) MarkDeliveryFailure(ctx context.Context, tenant string, worklogID uint64, causeSummary, responseCode string) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		worklog, err := s.repo.FindWorklogForUpdate(tx, tenant, worklogID)
		if err != nil {
			return err
		}
		if worklog.PushStatus != PushSending {
			return ErrInvalidTransition
		}
		attempt := worklog.PushAttempts + 1
		status := PushRetryWait
		var next *time.Time
		if retryAt, retryable := DeliveryRetryAt(s.clock.Now(), attempt); !retryable {
			status = PushDeadLetter
		} else {
			next = retryAt
		}
		summary := sanitizeError(causeSummary)
		if err = s.repo.UpdateWorklogDelivery(tx, tenant, worklogID, map[string]any{"push_status": status, "push_attempts": attempt, "next_retry_at": next, "last_error_summary": summary, "updated_at": s.clock.Now()}); err != nil {
			return err
		}
		return s.repo.CreateIntegrationAttempt(tx, &IntegrationAttempt{TenantID: tenant, WorklogID: worklogID, AttemptNo: attempt, Result: "FAILED", ErrorSummary: summary, ResponseCode: truncate(responseCode, 32), AttemptedAt: s.clock.Now()})
	})
}

func (s *Service) RetryDelivery(ctx context.Context, actor Actor, worklogID uint64) error {
	if !actor.Can("presale.worklog.retry") {
		return ErrForbidden
	}
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		worklog, err := s.repo.FindWorklogForUpdate(tx, actor.TenantID, worklogID)
		if err != nil {
			return err
		}
		requestValue, err := s.repo.FindRequest(tx, actor.TenantID, worklog.RequestID)
		if err != nil {
			return err
		}
		if err = s.requireReadable(tx, actor, requestValue); err != nil {
			return err
		}
		if worklog.PushStatus != PushRetryWait && worklog.PushStatus != PushDeadLetter {
			return ErrInvalidTransition
		}
		if err = s.repo.UpdateWorklogDelivery(tx, actor.TenantID, worklogID, map[string]any{"push_status": PushPending, "push_attempts": 0, "next_retry_at": nil, "last_error_summary": "", "updated_by": actor.UserID, "updated_at": s.clock.Now()}); err != nil {
			return err
		}
		return s.repo.RequeueOutboxByAggregate(tx, actor.TenantID, "presale_worklog", fmt.Sprint(worklogID))
	})
}

func (s *Service) Delivery(ctx context.Context, actor Actor, worklogID uint64) (DeliveryView, error) {
	if !actor.Can("presale.read") {
		return DeliveryView{}, ErrForbidden
	}
	worklog, err := s.repo.FindWorklog(ctx, actor.TenantID, worklogID)
	if err != nil {
		return DeliveryView{}, err
	}
	requestValue, err := s.repo.FindRequest(ctx, actor.TenantID, worklog.RequestID)
	if err != nil {
		return DeliveryView{}, err
	}
	if err = s.requireReadable(ctx, actor, requestValue); err != nil {
		return DeliveryView{}, err
	}
	return DeliveryView{WorklogID: worklog.ID, Status: worklog.PushStatus, Attempts: worklog.PushAttempts, NextRetryAt: worklog.NextRetryAt, LastErrorSummary: worklog.LastErrorSummary}, nil
}

func sanitizeError(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return truncate(strings.TrimSpace(value), 1000)
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
