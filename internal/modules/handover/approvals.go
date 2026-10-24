package handover

import (
	"context"
	"strings"
	"time"
)

// PresaleApprovalResolver 由审批引擎侧回答"该用户此刻是否是某审批实例当前节点的待办审批人"。
// 审批人由引擎计算、不在 CRM 本地库中，因此本地实现不得凭空编造审批人。
type PresaleApprovalResolver interface {
	ResolveCurrentApprover(ctx context.Context, tenantID, engineInstanceID, approverID string, node uint8) (bool, error)
}

// ApprovalInstance 是本地记录的审批实例候选（审批人需向引擎确认）。
type ApprovalInstance struct {
	InstanceID       uint64    `json:"-"`
	RequestID        uint64    `json:"-"`
	EngineInstanceID string    `json:"-"`
	CurrentNode      uint8     `json:"-"`
	Title            string    `json:"-"`
	Version          uint64    `json:"-"`
	UpdatedAt        time.Time `json:"-"`
}

// SetApprovalResolver 注入审批引擎解析器（组合根调用）。
func (service *Service) SetApprovalResolver(resolver PresaleApprovalResolver) {
	service.approvalResolver = resolver
}

// approvalResponsibilities 把待审实例按引擎结论折算成"该用户的审批责任"。
// 未接线解析器时不产出审批责任（不猜测审批人）；引擎查询失败则 失败时安全拒绝 上报。
func (service *Service) approvalResponsibilities(ctx context.Context, tenantID, userID string) ([]Responsibility, error) {
	if service.approvalResolver == nil {
		return nil, nil
	}
	instances, err := service.repository.ListPendingApprovalInstances(ctx, tenantID, 200)
	if err != nil {
		return nil, err
	}
	items := make([]Responsibility, 0, len(instances))
	for _, instance := range instances {
		if strings.TrimSpace(instance.EngineInstanceID) == "" {
			continue
		}
		match, err := service.approvalResolver.ResolveCurrentApprover(ctx, tenantID, instance.EngineInstanceID, userID, instance.CurrentNode)
		if err != nil {
			return nil, err
		}
		if !match {
			continue
		}
		items = append(items, Responsibility{
			ResourceType: ResourcePresaleApproval,
			ResourceID:   "presale-approval:" + instance.EngineInstanceID,
			ResourceName: instance.Title, OwnerUserID: userID,
			Category: CategoryPresaleApproval, Version: instance.Version, UpdatedAt: instance.UpdatedAt,
		})
	}
	return items, nil
}
