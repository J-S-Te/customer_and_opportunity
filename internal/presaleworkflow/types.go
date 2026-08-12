package presaleworkflow

import (
	"context"
	"fmt"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue                  = "customer-opportunity-presale"
	StartApprovalWorkflowName  = "crm-presale-approval-start"
	ApprovalActionWorkflowName = "crm-presale-approval-action"
	WorklogWorkflowName        = "crm-presale-worklog-publish"
	ActivityStartApproval      = "StartApproval"
	ActivityApprovalAction     = "ApprovalAction"
	ActivityPublishWorklog     = "PublishWorklog"
)

type EventInput struct {
	Event presale.OutboxEvent `json:"event"`
}

type Activities struct {
	Approval presale.ApprovalCommandPort
	PMS      presale.PMSPublisher
}

func (a *Activities) StartApproval(ctx context.Context, in EventInput) (presale.ApprovalStartResult, error) {
	if a == nil || a.Approval == nil {
		return presale.ApprovalStartResult{}, fmt.Errorf("presale approval activity is not configured")
	}
	return a.Approval.Start(ctx, in.Event)
}

func (a *Activities) ApprovalAction(ctx context.Context, in EventInput) error {
	if a == nil || a.Approval == nil {
		return fmt.Errorf("presale approval activity is not configured")
	}
	return a.Approval.Act(ctx, in.Event)
}

func (a *Activities) PublishWorklog(ctx context.Context, in EventInput) (string, error) {
	if a == nil || a.PMS == nil {
		return "", fmt.Errorf("presale PMS activity is not configured")
	}
	return a.PMS.PublishWorklog(ctx, in.Event)
}

func activityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    8,
		},
	}
}

func StartApprovalWorkflow(ctx workflow.Context, in EventInput) (presale.ApprovalStartResult, error) {
	var result presale.ApprovalStartResult
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, activityOptions()), ActivityStartApproval, in).Get(ctx, &result)
	return result, err
}

func ApprovalActionWorkflow(ctx workflow.Context, in EventInput) error {
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, activityOptions()), ActivityApprovalAction, in).Get(ctx, nil)
}

func WorklogWorkflow(ctx workflow.Context, in EventInput) (string, error) {
	var result string
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, activityOptions()), ActivityPublishWorklog, in).Get(ctx, &result)
	return result, err
}

func Register(w interface {
	RegisterWorkflowWithOptions(interface{}, workflow.RegisterOptions)
	RegisterActivity(interface{})
}, activities *Activities) {
	// Kept as a small adapter so tests can use a Temporal worker without exposing
	// the rest of the presale worker implementation.
	w.RegisterWorkflowWithOptions(StartApprovalWorkflow, workflow.RegisterOptions{Name: StartApprovalWorkflowName})
	w.RegisterWorkflowWithOptions(ApprovalActionWorkflow, workflow.RegisterOptions{Name: ApprovalActionWorkflowName})
	w.RegisterWorkflowWithOptions(WorklogWorkflow, workflow.RegisterOptions{Name: WorklogWorkflowName})
	if activities != nil {
		w.RegisterActivity(activities)
	}
}

type Client struct {
	Temporal  client.Client
	TaskQueue string
}

func (c Client) execute(ctx context.Context, event presale.OutboxEvent, workflowName string, result any) error {
	if c.Temporal == nil {
		return fmt.Errorf("temporal client is not configured")
	}
	if event.EventID == "" {
		return fmt.Errorf("presale event id is required")
	}
	workflowID := "crm-presale:" + workflowName + ":" + event.EventID
	run, err := c.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    workflowID,
		TaskQueue:             c.TaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, workflowName, EventInput{Event: event})
	if err != nil {
		// A retry after a process/network interruption may find the already-started
		// workflow. Reading it is safe because the event ID is the idempotency key.
		run = c.Temporal.GetWorkflow(ctx, workflowID, "")
	}
	return run.Get(ctx, result)
}

func (c Client) Start(ctx context.Context, event presale.OutboxEvent) (presale.ApprovalStartResult, error) {
	var result presale.ApprovalStartResult
	err := c.execute(ctx, event, StartApprovalWorkflowName, &result)
	return result, err
}

func (c Client) Act(ctx context.Context, event presale.OutboxEvent) error {
	return c.execute(ctx, event, ApprovalActionWorkflowName, nil)
}

func (c Client) PublishWorklog(ctx context.Context, event presale.OutboxEvent) (string, error) {
	var result string
	err := c.execute(ctx, event, WorklogWorkflowName, &result)
	return result, err
}
