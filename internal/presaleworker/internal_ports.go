package presaleworker

import (
	"context"
	"fmt"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

// internalPorts is used only by the explicitly enabled Temporal internal mode.
// The approval state is projected by the CRM service; these deterministic
// receipts keep the outbox/Temporal activities idempotent without requiring an
// external approval engine or PMS during the migration period.
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
