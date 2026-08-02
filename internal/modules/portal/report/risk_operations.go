package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrRiskAlertNotFound       = apperror.New(http.StatusNotFound, "PORTAL_REPORT_RISK_ALERT_NOT_FOUND", "report risk alert not found")
	ErrRiskReviewInvalid       = apperror.New(http.StatusUnprocessableEntity, "PORTAL_REPORT_RISK_REVIEW_INVALID", "report risk review is invalid")
	ErrRiskReviewConflict      = apperror.New(http.StatusConflict, "PORTAL_REPORT_RISK_REVIEW_CONFLICT", "report risk alert has already been reviewed or the idempotency key conflicts")
	ErrRiskGrantCannotUnfreeze = apperror.New(http.StatusConflict, "PORTAL_REPORT_RISK_GRANT_CANNOT_UNFREEZE", "frozen grant cannot be restored; revoke it and ask the customer to create a new authorization")
)

const maxRiskReviewReasonBytes = 500

type RiskAlertView struct {
	AlertID          string     `json:"alert_id"`
	RequestID        uint64     `json:"request_id"`
	RequestNo        string     `json:"request_no"`
	ReportType       string     `json:"report_type"`
	AccountID        string     `json:"account_id,omitempty"`
	RiskCode         string     `json:"risk_code"`
	Status           string     `json:"status"`
	DetectedAt       time.Time  `json:"detected_at"`
	AcknowledgedAt   *time.Time `json:"acknowledged_at,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy       string     `json:"resolved_by,omitempty"`
	ResolutionAction string     `json:"resolution_action,omitempty"`
	ResolutionReason string     `json:"resolution_reason,omitempty"`
	Version          uint64     `json:"version"`
}

type RiskReviewCommand struct {
	ExpectedVersion uint64
	Action          string
	Reason          string
	IdempotencyKey  string
}

// ListRiskAlerts returns only the current Portal account's alert evidence.
// AccountID and operator identity are removed from the browser projection.
func (s *DownloadService) ListRiskAlerts(ctx context.Context, actor Actor, openOnly bool, page, pageSize int) (pagination.Page[RiskAlertView], error) {
	actor.TenantID, actor.AccountID = strings.TrimSpace(actor.TenantID), strings.TrimSpace(actor.AccountID)
	if !validActor(actor) {
		return pagination.Page[RiskAlertView]{}, ErrRiskAlertNotFound
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	result, err := s.repo.ListRiskAlerts(ctx, actor, openOnly, page, pageSize)
	for index := range result.Items {
		result.Items[index].AccountID = ""
		result.Items[index].ResolvedBy = ""
		result.Items[index].ResolutionReason = ""
	}
	return result, err
}

// ListRiskAlertsForReview is machine-only and tenant scoped. It exposes the
// account identifier needed by an authorized reviewer but never token/object
// metadata, IP/device digests or provider error text.
func (s *DownloadService) ListRiskAlertsForReview(ctx context.Context, tenantID, status string, page, pageSize int) (pagination.Page[RiskAlertView], error) {
	tenantID, status = strings.TrimSpace(tenantID), strings.ToUpper(strings.TrimSpace(status))
	if !validBoundedText(tenantID, maxTenantIDBytes) || (status != "" && status != RiskAlertOpen && status != RiskAlertResolved) {
		return pagination.Page[RiskAlertView]{}, ErrRiskReviewInvalid
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	return s.repo.ListRiskAlertsForReview(ctx, tenantID, status, page, pageSize)
}

// ReviewRiskAlert resolves one OPEN alert under a row lock. UNFREEZE restores
// only the exact, unexpired frozen grant and is rejected if another active
// grant already exists. REVOKE_AND_REISSUE never manufactures a plaintext
// credential; it revokes the old grant and tells the customer UI to create a
// new one on its next explicit click.
func (s *DownloadService) ReviewRiskAlert(ctx context.Context, tenantID, operatorID, alertID string, command RiskReviewCommand) (*RiskAlertView, error) {
	tenantID, operatorID, alertID = strings.TrimSpace(tenantID), strings.TrimSpace(operatorID), strings.TrimSpace(alertID)
	command.Action = strings.ToUpper(strings.TrimSpace(command.Action))
	command.Reason, command.IdempotencyKey = strings.TrimSpace(command.Reason), strings.TrimSpace(command.IdempotencyKey)
	if !validBoundedText(tenantID, maxTenantIDBytes) || !validBoundedText(operatorID, maxAccountIDBytes) ||
		!validBoundedText(alertID, 64) || command.ExpectedVersion == 0 ||
		(command.Action != RiskActionUnfreeze && command.Action != RiskActionRevokeAndReissue) ||
		!validNarrative(command.Reason, maxRiskReviewReasonBytes) || !validBoundedText(command.IdempotencyKey, maxIdempotencyKeyBytes) {
		return nil, ErrRiskReviewInvalid
	}
	idempotencyHash := sourceHash("RISK_REVIEW", command.IdempotencyKey)
	payloadRaw, _ := json.Marshal([]any{tenantID, operatorID, alertID, command.ExpectedVersion, command.Action, command.Reason})
	payloadHash := sourceHash("RISK_REVIEW_PAYLOAD", string(payloadRaw))
	var view *RiskAlertView
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		alert, err := s.repo.FindRiskAlertForUpdate(tx, tenantID, alertID)
		if err != nil {
			return err
		}
		if replay, replayErr := s.repo.FindRiskReviewEvent(tx, tenantID, operatorID, idempotencyHash); replayErr == nil {
			if replay.AlertID != alert.ID || replay.Action != command.Action || replay.PayloadHash != payloadHash {
				return ErrRiskReviewConflict
			}
			view, err = s.repo.FindRiskAlertView(tx, tenantID, alert.PublicID)
			return err
		} else if !errors.Is(replayErr, ErrRiskAlertNotFound) {
			return replayErr
		}
		if alert.Status != RiskAlertOpen || alert.Version != command.ExpectedVersion || alert.ActiveSlot == nil {
			return ErrRiskReviewConflict
		}
		grant, err := s.repo.FindGrantByIDForUpdate(tx, tenantID, alert.GrantID)
		if err != nil || grant.CustomerID != alert.CustomerID || grant.RequestID != alert.RequestID || grant.AccountID != alert.AccountID || grant.Status != GrantFrozen || grant.RiskState != alert.RiskCode {
			return ErrRiskReviewConflict
		}
		now := s.clock.Now().UTC()
		grantFields := map[string]any{"updated_by": operatorID, "updated_at": now}
		eventType := "GRANT_UNFROZEN"
		if command.Action == RiskActionUnfreeze {
			if !now.Before(grant.ExpiresAt) {
				return ErrRiskGrantCannotUnfreeze
			}
			if active, activeErr := s.repo.FindActiveGrantForUpdate(tx, tenantID, alert.CustomerID, alert.RequestID, alert.AccountID); activeErr == nil && active.ID != grant.ID {
				return ErrRiskGrantCannotUnfreeze
			} else if activeErr != nil && !errors.Is(activeErr, ErrGrantNotFound) {
				return activeErr
			}
			grantFields["status"], grantFields["active_slot"], grantFields["risk_state"] = GrantActive, "ACTIVE", ""
		} else {
			grantFields["status"], grantFields["active_slot"] = GrantRevoked, nil
			eventType = "GRANT_REVOKED_FOR_REISSUE"
		}
		if err = s.repo.UpdateGrant(tx, grant, grantFields); err != nil {
			return err
		}
		if err = s.repo.UpdateRiskAlert(tx, alert, map[string]any{
			"status": RiskAlertResolved, "active_slot": nil, "resolved_at": &now,
			"resolved_by": operatorID, "resolution_action": command.Action,
			"resolution_reason": command.Reason,
		}); err != nil {
			return err
		}
		if err = s.repo.CreateRiskReviewEvent(tx, &RiskReviewEvent{
			TenantID: tenantID, AlertID: alert.ID, ActorID: operatorID, Action: command.Action,
			IdempotencyHash: idempotencyHash, PayloadHash: payloadHash,
			RequestTrace: strings.TrimSpace(requestctx.ID(tx)), OccurredAt: now,
		}); err != nil {
			return err
		}
		if err = s.repo.CreateDownloadEvent(tx, &DownloadEvent{
			TenantID: tenantID, CustomerID: alert.CustomerID, RequestID: alert.RequestID,
			GrantID: &grant.ID, AccountID: alert.AccountID, EventType: eventType, Result: "SUCCESS",
			ReasonCode: alert.RiskCode, RequestTrace: strings.TrimSpace(requestctx.ID(tx)),
			IdempotencyHash: idempotencyHash, OccurredAt: now,
		}); err != nil {
			return err
		}
		view, err = s.repo.FindRiskAlertView(tx, tenantID, alert.PublicID)
		return err
	})
	return view, err
}
