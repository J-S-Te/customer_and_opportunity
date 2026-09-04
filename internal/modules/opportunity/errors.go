package opportunity

import (
	"errors"
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	ErrNotFound              = apperror.New(http.StatusNotFound, "CRM_OPPORTUNITY_NOT_FOUND", "opportunity not found")
	ErrInvalidStage          = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_INVALID_STAGE", "invalid opportunity stage")
	ErrContractRequired      = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_CONTRACT_REQUIRED", "a contract is required for a manual signed transition")
	ErrLostReasonRequired    = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_LOST_REASON_REQUIRED", "a standard lost reason is required for a manual failed transition")
	ErrStaleEvent            = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_STALE_EXTERNAL_EVENT", "external event is older than the current external status")
	ErrVersionConflict       = apperror.ErrVersionConflict
	ErrCustomerForbidden     = apperror.New(http.StatusForbidden, "CRM_OPPORTUNITY_CUSTOMER_FORBIDDEN", "customer is outside the current data scope or is not active")
	ErrTerminalTodoAbsent    = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_TERMINAL_TODO_ABSENT", "opportunity has no matching terminal todo")
	ErrInactive              = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_INACTIVE", "void opportunities cannot be changed")
	ErrNotVoid               = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_NOT_VOID", "only void opportunities can be restored")
	ErrVoidBlocked           = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_VOID_BLOCKED", "opportunity has active dependencies")
	ErrOwnerRequired         = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_OWNER_REQUIRED", "owner platform subject is required")
	ErrInvalidMemberRole     = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_INVALID_MEMBER_ROLE", "opportunity member role is not supported")
	ErrInvalidTeamMember     = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_MEMBER_INVALID", "opportunity team member is not an active authorized platform user")
	ErrDuplicateMember       = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_DUPLICATE_MEMBER", "an opportunity team contains the same platform subject more than once")
	ErrTeamTooLarge          = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_TEAM_TOO_LARGE", "an opportunity team may contain at most 50 members")
	ErrIdempotencyRequired   = apperror.New(http.StatusBadRequest, "COMMON_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
	ErrIdempotencyConflict   = apperror.New(http.StatusConflict, "COMMON_IDEMPOTENCY_KEY_CONFLICT", "Idempotency-Key was already used with a different request")
	ErrIdempotencyKeyTooLong = apperror.New(http.StatusBadRequest, "COMMON_IDEMPOTENCY_KEY_INVALID", "Idempotency-Key header is too long")
	ErrInvalidAlertRule      = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_INVALID_ALERT_RULE", "stage alert rule is invalid")
	ErrPresaleIneligible     = errors.New("opportunity is not eligible for a new presale request")
	ErrAlertNotFound         = apperror.New(http.StatusNotFound, "CRM_OPPORTUNITY_ALERT_NOT_FOUND", "opportunity stage alert not found")
	ErrAlertNotReadable      = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_ALERT_NOT_READABLE", "opportunity stage alert is not unread")
	ErrInvalidQuery          = apperror.New(http.StatusBadRequest, "CRM_OPPORTUNITY_QUERY_INVALID", "opportunity query is invalid")
	ErrExternalStatusAbsent  = apperror.New(http.StatusNotFound, "CRM_OPPORTUNITY_EXTERNAL_STATUS_NOT_FOUND", "opportunity has no external quotation or bid status")
	ErrContractTransferState = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_CONTRACT_TRANSFER_NOT_READY", "only a signed opportunity with a verified contract can be transferred")
	ErrAttachmentNotFound    = apperror.New(http.StatusNotFound, "CRM_OPPORTUNITY_ATTACHMENT_NOT_FOUND", "opportunity attachment not found")
	ErrAttachmentInvalid     = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_ATTACHMENT_INVALID", "opportunity attachment metadata is invalid")
	ErrAttachmentUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_OPPORTUNITY_ATTACHMENT_UNAVAILABLE", "trusted attachment storage or scanning is not configured")
	ErrAttachmentNotReady    = apperror.New(http.StatusConflict, "CRM_OPPORTUNITY_ATTACHMENT_NOT_READY", "opportunity attachment is not available for this operation")
	ErrAttachmentRejected    = apperror.New(http.StatusUnprocessableEntity, "CRM_OPPORTUNITY_ATTACHMENT_REJECTED", "opportunity attachment was rejected by security scanning")
)
