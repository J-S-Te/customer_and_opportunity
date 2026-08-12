package presaleworkflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"go.temporal.io/sdk/testsuite"
)

type testPorts struct{}

func (testPorts) Start(context.Context, presale.OutboxEvent) (presale.ApprovalStartResult, error) {
	return presale.ApprovalStartResult{EngineInstanceID: "instance-1", EventSequence: 1}, nil
}

func (testPorts) Act(context.Context, presale.OutboxEvent) error { return nil }

func (testPorts) PublishWorklog(context.Context, presale.OutboxEvent) (string, error) {
	return "accepted", nil
}

func TestStartApprovalWorkflowRunsActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity((&Activities{Approval: testPorts{}}).StartApproval)

	env.ExecuteWorkflow(StartApprovalWorkflow, EventInput{Event: presale.OutboxEvent{EventID: "event-1"}})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result presale.ApprovalStartResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "instance-1", result.EngineInstanceID)
	require.Equal(t, uint64(1), result.EventSequence)
}

func TestWorklogWorkflowReturnsActivityResponse(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity((&Activities{PMS: testPorts{}}).PublishWorklog)

	env.ExecuteWorkflow(WorklogWorkflow, EventInput{Event: presale.OutboxEvent{EventID: "event-2"}})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result string
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "accepted", result)
}
