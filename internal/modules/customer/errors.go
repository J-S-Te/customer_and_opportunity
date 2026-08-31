package customer

import (
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	ErrNotFound                     = apperror.New(http.StatusNotFound, "CRM_CUSTOMER_NOT_FOUND", "customer not found")
	ErrDuplicateCode                = apperror.New(http.StatusConflict, "CRM_CUSTOMER_DUPLICATE_CREDIT_CODE", "unified credit code already exists")
	ErrDuplicateName                = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_DUPLICATE_NAME", "potential duplicate customer name requires an authorized override")
	ErrInvalidContact               = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_INVALID_REGISTRATION_CONTACT", "exactly one registration contact is required")
	ErrInactive                     = apperror.New(http.StatusConflict, "CRM_CUSTOMER_INACTIVE", "only active customers can be edited")
	ErrNotVoid                      = apperror.New(http.StatusConflict, "CRM_CUSTOMER_NOT_VOID", "only void customers can be restored")
	ErrVoidBlocked                  = apperror.New(http.StatusConflict, "CRM_CUSTOMER_VOID_BLOCKED", "customer has active dependencies")
	ErrVersionConflict              = apperror.ErrVersionConflict
	ErrMergeSameCustomer            = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_MERGE_SAME_CUSTOMER", "source and target customers must be different")
	ErrMergeInactive                = apperror.New(http.StatusConflict, "CRM_CUSTOMER_MERGE_REQUIRES_ACTIVE", "source and target customers must both be active")
	ErrMergeBlocked                 = apperror.New(http.StatusConflict, "CRM_CUSTOMER_MERGE_BLOCKED", "customer merge has dependencies that cannot be migrated safely")
	ErrMergeUnavailable             = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_MERGE_UNAVAILABLE", "customer merge repository is not configured")
	ErrIdempotencyRequired          = apperror.New(http.StatusUnprocessableEntity, "COMMON_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
	ErrIdempotencyConflict          = apperror.New(http.StatusConflict, "COMMON_IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different request")
	ErrIdempotencyInvalid           = apperror.New(http.StatusUnprocessableEntity, "COMMON_IDEMPOTENCY_KEY_INVALID", "Idempotency-Key must not exceed 128 characters")
	ErrCreateReplayInvalid          = apperror.New(http.StatusConflict, "CRM_CUSTOMER_CREATE_REPLAY_INVALID", "customer creation replay no longer matches its resource")
	ErrCreateIdempotencyUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_CREATE_IDEMPOTENCY_UNAVAILABLE", "customer creation idempotency repository is not configured")
	ErrInvalidQuery                 = apperror.New(http.StatusBadRequest, "CRM_CUSTOMER_QUERY_INVALID", "customer query is invalid")
	ErrKeyFilterUnavailable         = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_KEY_FILTER_NOT_CONFIGURED", "key-customer classification is not configured")
	ErrProjectHistoryUnavailable    = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_PROJECT_HISTORY_NOT_CONFIGURED", "project snapshot reader is not configured")
	ErrProjectHistoryDependency     = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_PROJECT_HISTORY_UNAVAILABLE", "project snapshot dependency is unavailable")
	ErrInvalidStakeholders          = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_STAKEHOLDERS_INVALID", "customer stakeholders are invalid")
	ErrInvalidSystems               = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_SYSTEMS_INVALID", "customer information systems are invalid")
	ErrProfileUnavailable           = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_PROFILE_NOT_CONFIGURED", "customer profile repository is not configured")
	ErrImportScannerUnavailable     = apperror.New(http.StatusServiceUnavailable, "CRM_CUSTOMER_IMPORT_SCANNER_UNAVAILABLE", "customer import file scanner is not configured")
	ErrImportScanFailed             = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_IMPORT_FILE_REJECTED", "customer import file was rejected by the security scanner")
	ErrImportInvalidFile            = apperror.New(http.StatusUnprocessableEntity, "CRM_CUSTOMER_IMPORT_FILE_INVALID", "customer import workbook is invalid")
	ErrImportJobNotFound            = apperror.New(http.StatusNotFound, "CRM_CUSTOMER_IMPORT_JOB_NOT_FOUND", "customer import job not found")
	ErrImportJobExpired             = apperror.New(http.StatusConflict, "CRM_CUSTOMER_IMPORT_JOB_EXPIRED", "customer import preview has expired")
	ErrImportJobConflict            = apperror.New(http.StatusConflict, "CRM_CUSTOMER_IMPORT_JOB_CONFLICT", "customer import job cannot be committed in its current state")
)
