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

// PlatformRoleRevoker is intentionally separate from PlatformProvisioner. A
// caller holding this capability may only converge the Portal application role
// to DISABLED; it cannot provision users or grant access. Invitation-link
// revocation must not call this port because an invitation and Portal access
// have independent lifecycles.
type PlatformRoleRevoker interface {
	RevokePortalRole(context.Context, string, string) error
}

type PortalProvisioner interface {
	ProvisionMappingIdempotent(context.Context, ContactIdentity, ProvisionedIdentity, string) (PortalMapping, error)
}

type PortalMappingDisabler interface {
	DisableMapping(context.Context, string, uint64, string, string, string) error
}

// OperationProtector encrypts recovery snapshots and the one-time invitation
// token. Implementations must provide authenticated encryption and must never
// log plaintext or include it in errors.
type OperationProtector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}

type Clock interface{ Now() time.Time }
type RandomSource interface{ Bytes(int) ([]byte, error) }
