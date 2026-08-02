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
	ErrContactPhoneUnavailable = errors.New("CRM_PRESALE_CONTACT_PHONE_UNAVAILABLE")
	ErrDependencyUnavailable   = errors.New("INTEGRATION_DEPENDENCY_UNAVAILABLE")
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
	case errors.Is(err, ErrContactPhoneUnavailable):
		return apperror.New(http.StatusServiceUnavailable, "CRM_PRESALE_CONTACT_PHONE_UNAVAILABLE", "contact phone is unavailable")
	case errors.Is(err, ErrDependencyUnavailable):
		return apperror.New(http.StatusServiceUnavailable, "INTEGRATION_DEPENDENCY_UNAVAILABLE", "integration dependency is unavailable")
	default:
		return err
	}
}
