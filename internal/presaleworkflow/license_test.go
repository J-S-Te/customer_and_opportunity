package presaleworkflow

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"testing"
	"time"
)

func TestTemporalActivitiesRecheckAtExecution(t *testing.T) {
	expired := false
	activities := &Activities{Approval: testPorts{}, PMS: testPorts{}, LicenseCheck: func(context.Context, core.Operation) error {
		if expired {
			return errors.New("expired")
		}
		return nil
	}}
	if _, err := activities.StartApproval(context.Background(), EventInput{}); err != nil {
		t.Fatal(err)
	}
	expired = true
	var deferred *temporal.ApplicationError
	if _, err := activities.StartApproval(context.Background(), EventInput{}); !errors.As(err, &deferred) || deferred.Type() != licenseDeferredType {
		t.Fatal("start ignored expiry")
	}
	if err := activities.ApprovalAction(context.Background(), EventInput{}); !errors.As(err, &deferred) || deferred.Type() != licenseDeferredType {
		t.Fatal("action ignored expiry")
	}
	if _, err := activities.PublishWorklog(context.Background(), EventInput{}); !errors.As(err, &deferred) || deferred.Type() != licenseDeferredType {
		t.Fatal("publish ignored expiry")
	}
}

func TestLicenseDeferralWorkflowResumesSameEventAfterRenewal(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	expired := true
	calls := 0
	activities := &Activities{Approval: testPorts{}, LicenseCheck: func(context.Context, core.Operation) error {
		calls++
		if expired {
			return errors.New("expired")
		}
		return nil
	}}
	env.RegisterActivity(activities.StartApproval)
	env.RegisterDelayedCallback(func() { expired = false }, 30*time.Second)
	env.ExecuteWorkflow(StartApprovalWorkflow, EventInput{Event: presale.OutboxEvent{EventID: "stable-event"}})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil || calls < 2 {
		t.Fatalf("workflow failed to resume: %v calls %d", env.GetWorkflowError(), calls)
	}
}
