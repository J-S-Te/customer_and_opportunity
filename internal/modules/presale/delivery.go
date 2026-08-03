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

// Outbox worker 原子领取事件后先把本地投递投影切为 SENDING；此处不直接调用 PMS。
// 分离“领取/状态记录”和“外部调用”，便于 worker 在租约过期后安全恢复。
func (s *Service) MarkDeliverySending(ctx context.Context, tenant string, worklogID uint64) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		worklog, err := s.repo.FindWorklogForUpdate(tx, tenant, worklogID)
		if err != nil {
			return err
		}
		// worker 可能已更新投影却尚未确认 Outbox 就失去租约，因此重新领取同一事件时
		// 允许从 SENDING 继续，SUCCESS 也按幂等完成处理。
		if worklog.PushStatus == PushSending || worklog.PushStatus == PushSuccess {
			return nil
		}
		if worklog.PushStatus != PushPending && worklog.PushStatus != PushRetryWait {
			return ErrInvalidTransition
		}
		return s.repo.UpdateWorklogDelivery(tx, tenant, worklogID, map[string]any{"push_status": PushSending, "updated_at": s.clock.Now()})
	})
}

// 领域投影与 Outbox worker 共用同一退避表；attempt 从 1 开始，超出表长即进入死信。
func DeliveryRetryAt(now time.Time, attempt uint8) (*time.Time, bool) {
	if attempt == 0 || int(attempt) > len(retryDelays) {
		return nil, false
	}
	next := now.Add(retryDelays[attempt-1])
	return &next, true
}

// 只有真实 PMS 成功响应才能落为 SUCCESS；重复成功确认不重复累计尝试次数。
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

// 失败原因先去除换行并截断再持久化，避免日志注入和无限增长；
// 领域层给出确定性退避时间，基础设施调度时可额外加入有界抖动。
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
