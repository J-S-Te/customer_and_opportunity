package presale

import (
	"errors"
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	ErrNotFound                = errors.New("CRM_PRESALE_NOT_FOUND")
	ErrForbidden               = errors.New("CRM_PRESALE_FORBIDDEN")
	ErrInvalidFilter           = errors.New("CRM_PRESALE_INVALID_FILTER")
	ErrInvalidInput            = errors.New("CRM_PRESALE_INVALID_INPUT")
	ErrInvalidTransition       = errors.New("CRM_PRESALE_INVALID_TRANSITION")
	ErrVersionConflict         = errors.New("COMMON_VERSION_CONFLICT")
	ErrIdempotencyConflict     = errors.New("CRM_PRESALE_IDEMPOTENCY_CONFLICT")
	ErrInvalidApprovalEvent    = errors.New("CRM_PRESALE_INVALID_APPROVAL_EVENT")
	ErrReportExportUnavailable = errors.New("CRM_PRESALE_REPORT_EXPORT_UNAVAILABLE")
	ErrReportExportLimit       = errors.New("CRM_PRESALE_REPORT_EXPORT_LIMIT")
	ErrReportExportBusy        = errors.New("CRM_PRESALE_REPORT_EXPORT_BUSY")
	ErrContactPhoneUnavailable = errors.New("CRM_PRESALE_CONTACT_PHONE_UNAVAILABLE")
	ErrDependencyUnavailable   = errors.New("INTEGRATION_DEPENDENCY_UNAVAILABLE")
	ErrOpportunityNotEligible  = errors.New("CRM_PRESALE_OPPORTUNITY_NOT_ELIGIBLE")
	ErrTeamLeadInvalid         = errors.New("CRM_PRESALE_TEAM_LEAD_INVALID")
	ErrAssigneeNotQualified    = errors.New("CRM_PRESALE_ASSIGNEE_NOT_QUALIFIED")
)

func apiError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return apperror.New(http.StatusNotFound, "CRM_PRESALE_NOT_FOUND", "presale resource not found")
	case errors.Is(err, ErrForbidden):
		return apperror.New(http.StatusForbidden, "CRM_PRESALE_FORBIDDEN", "permission denied")
	case errors.Is(err, ErrInvalidFilter):
		return apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid presale query filter")
	case errors.Is(err, ErrInvalidInput):
		return apperror.New(http.StatusUnprocessableEntity, "CRM_PRESALE_INVALID_INPUT", "request validation failed")
	case errors.Is(err, ErrInvalidTransition):
		return apperror.New(http.StatusConflict, "CRM_PRESALE_INVALID_TRANSITION", "operation is not allowed in current status")
	case errors.Is(err, ErrVersionConflict):
		return apperror.ErrVersionConflict
	case errors.Is(err, ErrIdempotencyConflict):
		return apperror.New(http.StatusConflict, "CRM_PRESALE_IDEMPOTENCY_CONFLICT", "idempotency key was used with a different request")
	case errors.Is(err, ErrInvalidApprovalEvent):
		return apperror.New(http.StatusConflict, "CRM_PRESALE_INVALID_APPROVAL_EVENT", "approval callback is duplicated, stale or out of order")
	case errors.Is(err, ErrReportExportUnavailable):
		return apperror.New(http.StatusServiceUnavailable, "CRM_PRESALE_REPORT_EXPORT_UNAVAILABLE", "report export is not configured")
	case errors.Is(err, ErrReportExportLimit):
		return apperror.New(http.StatusUnprocessableEntity, "CRM_PRESALE_REPORT_EXPORT_LIMIT", "报表分布超过 10000 行，请缩小时间或人员、组织、商机范围")
	case errors.Is(err, ErrReportExportBusy):
		return apperror.New(http.StatusTooManyRequests, "CRM_PRESALE_REPORT_EXPORT_BUSY", "报表导出繁忙，请稍后重试")
	case errors.Is(err, ErrContactPhoneUnavailable):
		return apperror.New(http.StatusServiceUnavailable, "CRM_PRESALE_CONTACT_PHONE_UNAVAILABLE", "contact phone is unavailable")
	case errors.Is(err, ErrDependencyUnavailable):
		return apperror.New(http.StatusServiceUnavailable, "INTEGRATION_DEPENDENCY_UNAVAILABLE", "integration dependency is unavailable")
	case errors.Is(err, ErrOpportunityNotEligible):
		return apperror.New(http.StatusConflict, "CRM_PRESALE_OPPORTUNITY_NOT_ELIGIBLE", "presale support can only be requested for an active opportunity in a progressing stage")
	case errors.Is(err, ErrTeamLeadInvalid):
		return apperror.New(http.StatusConflict, "CRM_PRESALE_TEAM_LEAD_INVALID", "the assigned team lead is no longer active in the execution department; select a team lead again")
	case errors.Is(err, ErrAssigneeNotQualified):
		base := apperror.New(http.StatusUnprocessableEntity, "CRM_PRESALE_ASSIGNEE_NOT_QUALIFIED", "assignee does not hold the permissions required for this assignment; grant the required role before assigning")
		var qualification *assigneeNotQualifiedError
		if errors.As(err, &qualification) {
			return apperror.WithDetails(base, map[string]any{
				"assignee_id": qualification.AssigneeID, "required_permissions": qualification.RequiredPermissions,
			})
		}
		return base
	default:
		return err
	}
}
