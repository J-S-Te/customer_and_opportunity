package importprovision

import (
	"net/http"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

var (
	// ErrActorIdentityRequired 表示缺少 X-Actor-Identity-ID；
	// 该头只用于审计归属，不能替代 owner_identity_id 决定负责人。
	ErrActorIdentityRequired = apperror.New(http.StatusUnprocessableEntity, "CRM_IMPORT_PROVISION_ACTOR_INVALID", "缺少或无效的 X-Actor-Identity-ID，无法记录审计归属")
	// ErrInvalidRequest 表示请求体或路径参数无效。
	ErrInvalidRequest = apperror.New(http.StatusUnprocessableEntity, "CRM_IMPORT_PROVISION_INVALID_REQUEST", "历史合同导入开立请求无效")
	// ErrIdempotencyKeyRequired 与既有创建接口同码，便于合同侧统一处理重试。
	ErrIdempotencyKeyRequired = apperror.New(http.StatusUnprocessableEntity, "COMMON_IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
	// ErrUnavailable 表示通道依赖未接线（服务未装配）。
	ErrUnavailable = apperror.New(http.StatusServiceUnavailable, "CRM_IMPORT_PROVISION_UNAVAILABLE", "历史合同导入开立通道未装配，请稍后重试")
)
