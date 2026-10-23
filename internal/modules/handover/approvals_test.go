package handover

import (
	"context"
	"errors"
	"testing"
)

type approvalResolverStub struct {
	match bool
	err   error
	calls int
}

func (stub *approvalResolverStub) ResolveCurrentApprover(_ context.Context, _, _, _ string, _ uint8) (bool, error) {
	stub.calls++
	return stub.match, stub.err
}

// 审批责任必须由审批引擎确认：未接线不猜测审批人，引擎确认才计入，查询失败 失败时安全拒绝。
func TestApprovalResponsibilitiesRequireEngineConfirmation(t *testing.T) {
	repository := newRepositoryFake()
	repository.instances = []ApprovalInstance{{InstanceID: 1, RequestID: 2, EngineInstanceID: "engine-1", CurrentNode: 2, Title: "售前审批", Version: 3}}
	ctx := context.Background()

	unwired := serviceFor(t, repository)
	if items, _, err := unwired.Responsibilities(ctx, "tenant-1", "user-leaver", "", 50); err != nil || len(items) != 0 {
		t.Fatalf("without a resolver no approval responsibility may be produced: %d (%v)", len(items), err)
	}

	notApprover := serviceFor(t, repository)
	notApprover.SetApprovalResolver(&approvalResolverStub{match: false})
	if items, _, err := notApprover.Responsibilities(ctx, "tenant-1", "user-leaver", "", 50); err != nil || len(items) != 0 {
		t.Fatalf("engine said not the approver: %d (%v)", len(items), err)
	}

	approver := serviceFor(t, repository)
	approver.SetApprovalResolver(&approvalResolverStub{match: true})
	items, _, err := approver.Responsibilities(ctx, "tenant-1", "user-leaver", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Category != CategoryPresaleApproval || items[0].ResourceType != ResourcePresaleApproval ||
		items[0].ResourceID != "presale-approval:engine-1" || items[0].Version != 3 {
		t.Fatalf("approval responsibility wrong: %+v", items)
	}

	failing := serviceFor(t, repository)
	failing.SetApprovalResolver(&approvalResolverStub{err: errors.New("engine unavailable")})
	if _, _, err := failing.Responsibilities(ctx, "tenant-1", "user-leaver", "", 50); err == nil {
		t.Fatal("engine failure must fail closed instead of silently dropping the responsibility")
	}
}
