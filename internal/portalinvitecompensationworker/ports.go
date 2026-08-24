package portalinvitecompensationworker

import (
	"context"
	"errors"
	"strconv"

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

// httpBindingRepair 按任务类型把补偿任务路由到平台 BIND / DISABLE_BIND；customer_ref 沿用
// saga 的十进制 CustomerID 约定，与双写路径完全一致。
type httpBindingRepair struct {
	writer   *portalinvite.HTTPPlatformBindingWriter
	disabler *portalinvite.HTTPPlatformBindingDisabler
}

func (r httpBindingRepair) RepairBinding(ctx context.Context, task portalinvite.CompensationTask) error {
	customerRef := strconv.FormatUint(task.CustomerID, 10)
	switch task.TaskType {
	case portalinvite.CompensationBinding:
		if r.writer == nil {
			return errBindingRepairNotConfigured
		}
		return r.writer.BindCustomerIdempotent(ctx, task.PlatformUserID, customerRef, task.TaskNo)
	case portalinvite.CompensationBindingDisable:
		if r.disabler == nil {
			return errBindingRepairNotConfigured
		}
		return r.disabler.DisableCustomerBindingIdempotent(ctx, task.PlatformUserID, customerRef, task.TaskNo)
	default:
		return errBindingRepairNotConfigured
	}
}

var errBindingRepairNotConfigured = errors.New("platform customer binding repair adapter is not configured")

// httpBindingStatusReader 把平台绑定 BIND 客户端适配为对账只读端口（同一 scope，只读调用）。
type httpBindingStatusReader struct {
	writer *portalinvite.HTTPPlatformBindingWriter
}

func (r httpBindingStatusReader) BindingStatus(ctx context.Context, platformUserID string) (string, bool, error) {
	return r.writer.BindingStatus(ctx, platformUserID)
}
