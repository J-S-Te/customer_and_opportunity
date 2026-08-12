package account

import (
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	ErrNotProvisioned        = apperror.New(http.StatusForbidden, "PORTAL_IDENTITY_NOT_PROVISIONED", "portal identity is not provisioned")
	ErrIdentityDisabled      = apperror.New(http.StatusForbidden, "PORTAL_IDENTITY_DISABLED", "portal identity is disabled")
	ErrSubjectMismatch       = apperror.New(http.StatusForbidden, "PORTAL_IDENTITY_SUBJECT_MISMATCH", "signed-in account does not match the invitation")
	ErrInvalidClaims         = apperror.New(http.StatusUnauthorized, "PORTAL_OIDC_INVALID_CLAIMS", "OIDC claims are not valid for this application")
	ErrPortalAuthorization   = apperror.New(http.StatusForbidden, "PORTAL_AUTHORIZATION_REQUIRED", "portal_customer role and portal permissions are required")
	ErrInvalidLoginState     = apperror.New(http.StatusUnauthorized, "PORTAL_OIDC_INVALID_STATE", "login state is invalid or expired")
	ErrSessionNotFound       = apperror.New(http.StatusNotFound, "PORTAL_ACCOUNT_SESSION_NOT_FOUND", "portal session was not found")
	ErrSecurityEventNotFound = apperror.New(http.StatusNotFound, "PORTAL_ACCOUNT_SECURITY_EVENT_NOT_FOUND", "security event was not found")
	ErrVersionConflict       = apperror.New(http.StatusConflict, "PORTAL_IDENTITY_VERSION_CONFLICT", "portal identity version has changed")
)
