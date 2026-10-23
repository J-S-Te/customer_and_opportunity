package presaleworker

import (
	"context"
	"fmt"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

// internalPorts 仅用于显式启用的 Temporal 内部模式。
// 审批状态由 CRM 服务投影；这些确定性
// 回执保证 Outbox/Temporal 活动幂等，迁移期间
// 无需外部审批引擎或 PMS。
type internalPorts struct{}

func (internalPorts) Start(_ context.Context, event presale.OutboxEvent) (presale.ApprovalStartResult, error) {
	if event.EventID == "" {
		return presale.ApprovalStartResult{}, fmt.Errorf("presale event id is required")
	}
	return presale.ApprovalStartResult{EngineInstanceID: "crm-temporal-" + event.EventID, EventSequence: 1}, nil
}

func (internalPorts) Act(_ context.Context, event presale.OutboxEvent) error {
	if event.EventID == "" {
		return fmt.Errorf("presale event id is required")
	}
	return nil
}

func (internalPorts) PublishWorklog(_ context.Context, event presale.OutboxEvent) (string, error) {
	if event.EventID == "" {
		return "", fmt.Errorf("presale event id is required")
	}
	return "crm-temporal-worklog-" + event.EventID, nil
}
