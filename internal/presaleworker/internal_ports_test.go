package presaleworker

import (
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func TestInternalPortsReturnDeterministicReceipts(t *testing.T) {
	event := presale.OutboxEvent{EventID: "evt-42"}
	ports := internalPorts{}
	started, err := ports.Start(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if started.EngineInstanceID != "crm-temporal-evt-42" || started.EventSequence != 1 {
		t.Fatalf("unexpected approval receipt: %+v", started)
	}
	if err := ports.Act(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	receipt, err := ports.PublishWorklog(context.Background(), event)
	if err != nil || receipt != "crm-temporal-worklog-evt-42" {
		t.Fatalf("receipt=%q err=%v", receipt, err)
	}
}

func TestInternalPortsRejectMissingEventID(t *testing.T) {
	ports := internalPorts{}
	if _, err := ports.Start(context.Background(), presale.OutboxEvent{}); err == nil {
		t.Fatal("expected missing event id error")
	}
}
