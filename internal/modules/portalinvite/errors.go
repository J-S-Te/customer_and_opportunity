package portalinvite

import (
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	ErrInvalidArgument       = apperror.New(http.StatusBadRequest, "CRM_PORTAL_INVITE_INVALID_ARGUMENT", "portal invitation request is invalid")
	ErrContactInvalid        = apperror.New(http.StatusUnprocessableEntity, "CRM_PORTAL_INVITE_CONTACT_INVALID", "customer must have exactly one valid registration contact")
	ErrNotFound              = apperror.New(http.StatusNotFound, "CRM_PORTAL_INVITE_NOT_FOUND", "portal invitation not found")
	ErrExpired               = apperror.New(http.StatusGone, "CRM_PORTAL_INVITE_EXPIRED", "portal invitation expired")
	ErrUsed                  = apperror.New(http.StatusConflict, "CRM_PORTAL_INVITE_USED", "portal invitation was already used")
	ErrRevoked               = apperror.New(http.StatusConflict, "CRM_PORTAL_INVITE_REVOKED", "portal invitation was revoked")
	ErrSubjectMismatch       = apperror.New(http.StatusForbidden, "CRM_PORTAL_INVITE_SUBJECT_MISMATCH", "authenticated platform user does not match invitation")
	ErrVersionConflict       = apperror.New(http.StatusConflict, "CRM_PORTAL_INVITE_VERSION_CONFLICT", "portal invitation was changed concurrently")
	ErrDependencyUnavailable = apperror.New(http.StatusServiceUnavailable, "INTEGRATION_DEPENDENCY_UNAVAILABLE", "portal identity provisioning dependency is unavailable")
	ErrIdempotencyRequired   = apperror.New(http.StatusUnprocessableEntity, "COMMON_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
	ErrIdempotencyInvalid    = apperror.New(http.StatusUnprocessableEntity, "COMMON_IDEMPOTENCY_KEY_INVALID", "Idempotency-Key must contain 1 to 128 visible characters")
	ErrIdempotencyConflict   = apperror.New(http.StatusConflict, "COMMON_IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different request")
)
