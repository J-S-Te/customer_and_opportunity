package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

type approvalTaskResolverStub struct {
	task  presale.ApprovalTask
	err   error
	calls int
	node  uint8
}

func (stub *approvalTaskResolverStub) ResolveCurrentTask(_ context.Context, query presale.ApprovalTaskQuery) (presale.ApprovalTask, error) {
	stub.calls++
	stub.node = query.Node
	return stub.task, stub.err
}

// 只有引擎确认"该用户是该实例当前节点的待办审批人"时才计入交接责任。
func TestHandoverApprovalResolverRequiresEngineConfirmation(t *testing.T) {
	resolver := &approvalTaskResolverStub{task: presale.ApprovalTask{EngineTaskID: "task-1", EngineInstanceID: "engine-1", ApproverID: "user-leaver", Node: 2}}
	adapter := handoverApprovalResolver{resolver: resolver}
	match, err := adapter.ResolveCurrentApprover(context.Background(), "tenant-1", "engine-1", "user-leaver", 2)
	if err != nil || !match || resolver.node != 2 {
		t.Fatalf("match=%v err=%v node=%d", match, err, resolver.node)
	}

	mismatched := handoverApprovalResolver{resolver: &approvalTaskResolverStub{task: presale.ApprovalTask{EngineTaskID: "task-1", ApproverID: "user-other"}}}
	if match, err := mismatched.ResolveCurrentApprover(context.Background(), "tenant-1", "engine-1", "user-leaver", 2); err != nil || match {
		t.Fatalf("another approver's task must not match: match=%v err=%v", match, err)
	}

	empty := handoverApprovalResolver{resolver: &approvalTaskResolverStub{}}
	if match, err := empty.ResolveCurrentApprover(context.Background(), "tenant-1", "engine-1", "user-leaver", 2); err != nil || match {
		t.Fatalf("empty engine task must not match: match=%v err=%v", match, err)
	}
}

// 引擎故障必须上抛（清单侧 失败时安全拒绝），未配置解析器必须报错而不是静默放行。
func TestHandoverApprovalResolverFailsClosed(t *testing.T) {
	failing := handoverApprovalResolver{resolver: &approvalTaskResolverStub{err: errors.New("engine unavailable")}}
	if _, err := failing.ResolveCurrentApprover(context.Background(), "tenant-1", "engine-1", "user-1", 1); err == nil {
		t.Fatal("engine failure must propagate")
	}
	if _, err := (handoverApprovalResolver{}).ResolveCurrentApprover(context.Background(), "tenant-1", "engine-1", "user-1", 1); err == nil {
		t.Fatal("missing resolver must be reported")
	}
}
