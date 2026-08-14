package portalinvite

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type ContactIdentity struct {
	TenantID, CustomerName                              string
	CustomerID, ContactID                               uint64
	DisplayName, Phone, Email, PhoneMasked, EmailMasked string
}
type ProvisionedIdentity struct {
	PlatformUserID string
	AccountNo      string
}
type PortalMapping struct{ PortalAccountID string }

type PortalIdentitySnapshot struct {
	PlatformUserID  string  `json:"platform_user_id"`
	Found           bool    `json:"found"`
	PortalAccountID string  `json:"portal_account_id,omitempty"`
	AccountNo       string  `json:"account_no,omitempty"`
	CustomerID      uint64  `json:"customer_id,omitempty"`
	ContactID       *uint64 `json:"contact_id,omitempty"`
	Status          string  `json:"status,omitempty"`
	Version         uint64  `json:"version,omitempty"`
}

type CustomerReader interface {
	RegistrationContact(context.Context, auth.Principal, uint64) (ContactIdentity, error)
}

type CustomerAccessChecker interface {
	CanAccessCustomer(context.Context, auth.Principal, uint64) (bool, error)
}

type PlatformProvisioner interface {
	ProvisionExternalUser(context.Context, ContactIdentity) (ProvisionedIdentity, error)
	AssignPortalRoleIdempotent(context.Context, string, string) error
}

// PlatformRoleRevoker 与 PlatformProvisioner 刻意分权：持有该能力的调用方只能把门户应用角色
// 收敛为禁用，不能创建用户或授予访问。邀请链接撤销不能调用它，因为链接和门户访问生命周期独立。
type PlatformRoleRevoker interface {
	RevokePortalRole(context.Context, string, string) error
}

type PortalProvisioner interface {
	ProvisionMappingIdempotent(context.Context, ContactIdentity, ProvisionedIdentity, string) (PortalMapping, error)
}

type PortalMappingDisabler interface {
	DisableMapping(context.Context, string, uint64, string, string, string) error
}

// PlatformBindingWriter / PlatformBindingDisabler 是客户绑定收敛到平台后的双写适配器
// （Phase 2）。门户映射仍是权威，平台绑定调用失败不中断现有 saga，由对账路径按幂等键补齐。
type PlatformBindingWriter interface {
	BindCustomerIdempotent(context.Context, string, string, string) error
}

type PlatformBindingDisabler interface {
	DisableCustomerBindingIdempotent(context.Context, string, string, string) error
}

// OperationProtector 对恢复快照和一次性邀请令牌做认证加密；实现不得记录明文，也不得把明文带入错误。
type OperationProtector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}

type Clock interface{ Now() time.Time }
type RandomSource interface{ Bytes(int) ([]byte, error) }
