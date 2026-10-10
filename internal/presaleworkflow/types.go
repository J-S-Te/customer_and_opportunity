package presaleworkflow

import (
	"context"
	"errors"
	"fmt"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/commercial"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/workerlicense"
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
	licenseDeferredType        = "COMMERCIAL_LICENSE_DEFERRED"
)

type EventInput struct {
	Event presale.OutboxEvent `json:"event"`
}

type Activities struct {
	LicenseCheck commercial.Check
	Approval     presale.ApprovalCommandPort
	PMS          presale.PMSPublisher
}

func (a *Activities) StartApproval(ctx context.Context, in EventInput) (presale.ApprovalStartResult, error) {
	if a == nil || a.Approval == nil {
		return presale.ApprovalStartResult{}, fmt.Errorf("presale approval activity is not configured")
	}
	if err := workerlicense.RequireCheck(ctx, a.LicenseCheck, core.MUTATE_BUSINESS); err != nil {
		return presale.ApprovalStartResult{}, temporal.NewNonRetryableApplicationError("commercial license task deferred", licenseDeferredType, err)
	}
	return a.Approval.Start(ctx, in.Event)
}

func (a *Activities) ApprovalAction(ctx context.Context, in EventInput) error {
	if a == nil || a.Approval == nil {
		return fmt.Errorf("presale approval activity is not configured")
	}
	if err := workerlicense.RequireCheck(ctx, a.LicenseCheck, core.MUTATE_BUSINESS); err != nil {
		return temporal.NewNonRetryableApplicationError("commercial license task deferred", licenseDeferredType, err)
	}
	return a.Approval.Act(ctx, in.Event)
}

func (a *Activities) PublishWorklog(ctx context.Context, in EventInput) (string, error) {
	if a == nil || a.PMS == nil {
		return "", fmt.Errorf("presale PMS activity is not configured")
	}
	if err := workerlicense.RequireCheck(ctx, a.LicenseCheck, core.MUTATE_BUSINESS); err != nil {
		return "", temporal.NewNonRetryableApplicationError("commercial license task deferred", licenseDeferredType, err)
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
	err := executeLicensedActivity(ctx, ActivityStartApproval, in, &result)
	return result, err
}

func ApprovalActionWorkflow(ctx workflow.Context, in EventInput) error {
	return executeLicensedActivity(ctx, ActivityApprovalAction, in, nil)
}

func WorklogWorkflow(ctx workflow.Context, in EventInput) (string, error) {
	var result string
	err := executeLicensedActivity(ctx, ActivityPublishWorklog, in, &result)
	return result, err
}

// License deferral is not a business failure: keep the existing workflow and
// stable EventID pending, without consuming its external-operation retry budget.
// Use durable Temporal timers, never host sleeps or workflow clock reads.
func executeLicensedActivity(ctx workflow.Context, name string, in EventInput, result any) error {
	for {
		err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, activityOptions()), name, in).Get(ctx, result)
		var deferred *temporal.ApplicationError
		if err == nil || !errors.As(err, &deferred) || deferred.Type() != licenseDeferredType {
			return err
		}
		if err := workflow.Sleep(ctx, time.Minute); err != nil {
			return err
		}
	}
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
