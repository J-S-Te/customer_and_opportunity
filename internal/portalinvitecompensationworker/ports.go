package portalinvitecompensationworker

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
)

type httpRoleAssigner struct {
	client *portalinvite.HTTPPlatformRoleAssigner
}

func (p httpRoleAssigner) AssignPortalRole(ctx context.Context, task portalinvite.CompensationTask) error {
	return p.client.AssignPortalRole(ctx, task.PlatformUserID, task.TaskNo)
}

type httpMappingProvisioner struct {
	client *portalinvite.HTTPPortalProvisioner
}

func (p httpMappingProvisioner) ProvisionMapping(ctx context.Context, task portalinvite.CompensationTask) (portalinvite.PortalMapping, error) {
	contact := portalinvite.ContactIdentity{
		TenantID: task.TenantID, CustomerID: task.CustomerID, ContactID: task.ContactID,
	}
	identity := portalinvite.ProvisionedIdentity{PlatformUserID: task.PlatformUserID, AccountNo: task.AccountNo}
	return p.client.ProvisionMappingIdempotent(ctx, contact, identity, task.TaskNo)
}
