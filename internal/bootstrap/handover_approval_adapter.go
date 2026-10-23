package bootstrap

import (
	"context"
	"errors"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/handover"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

// handoverApprovalResolver 把交接的审批责任查询接到审批引擎解析器：
// 只有引擎确认该用户是当前节点待办审批人时，才计入其交接责任。
type handoverApprovalResolver struct {
	resolver presale.ApprovalTaskResolver
}

var _ handover.PresaleApprovalResolver = handoverApprovalResolver{}

func (adapter handoverApprovalResolver) ResolveCurrentApprover(ctx context.Context, tenantID, engineInstanceID, approverID string, node uint8) (bool, error) {
	if adapter.resolver == nil {
		return false, errors.New("approval task resolver is not configured")
	}
	task, err := adapter.resolver.ResolveCurrentTask(ctx, presale.ApprovalTaskQuery{
		TenantID: tenantID, EngineInstanceID: engineInstanceID, ApproverID: approverID, Node: node,
	})
	if err != nil {
		return false, err
	}
	// 引擎返回的任务必须确实属于该审批人与该实例；否则视为"不是当前审批人"。
	if strings.TrimSpace(task.EngineTaskID) == "" || task.ApproverID != approverID {
		return false, nil
	}
	return true, nil
}
