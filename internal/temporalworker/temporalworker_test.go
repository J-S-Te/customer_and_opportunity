package temporalworker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
)

type deploymentStub struct {
	current client.WorkerDeploymentSetCurrentVersionOptions
	ramping client.WorkerDeploymentSetRampingVersionOptions
}

func (stub *deploymentStub) Describe(context.Context, client.WorkerDeploymentDescribeOptions) (client.WorkerDeploymentDescribeResponse, error) {
	return client.WorkerDeploymentDescribeResponse{ConflictToken: []byte("conflict-1")}, nil
}
func (stub *deploymentStub) SetCurrentVersion(_ context.Context, value client.WorkerDeploymentSetCurrentVersionOptions) (client.WorkerDeploymentSetCurrentVersionResponse, error) {
	stub.current = value
	return client.WorkerDeploymentSetCurrentVersionResponse{}, nil
}
func (stub *deploymentStub) SetRampingVersion(_ context.Context, value client.WorkerDeploymentSetRampingVersionOptions) (client.WorkerDeploymentSetRampingVersionResponse, error) {
	stub.ramping = value
	return client.WorkerDeploymentSetRampingVersionResponse{}, nil
}

func TestWorkerOptionsEnableDeploymentVersioning(t *testing.T) {
	options, err := WorkerOptions(VersioningConfig{Enabled: true, DeploymentName: "customer-opportunity-presale", BuildID: "presale-v2", Policy: "PINNED"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.DeploymentOptions.UseVersioning || options.DeploymentOptions.Version.BuildID != "presale-v2" || options.DeploymentOptions.DefaultVersioningBehavior != workflow.VersioningBehaviorPinned {
		t.Fatalf("options = %#v", options)
	}
}

func TestRolloutUsesConflictTokenAndPollerProtection(t *testing.T) {
	stub := &deploymentStub{}
	if err := PromoteCurrent(context.Background(), stub, "presale-v2", "release-42"); err != nil {
		t.Fatal(err)
	}
	if string(stub.current.ConflictToken) != "conflict-1" || stub.current.AllowNoPollers || stub.current.IgnoreMissingTaskQueues {
		t.Fatalf("promote options = %#v", stub.current)
	}
	for _, percentage := range []float32{5, 25, 50, 100} {
		if err := RampVersion(context.Background(), stub, "presale-v2", "release-42", percentage); err != nil || stub.ramping.Percentage != percentage || stub.ramping.IgnoreMissingTaskQueues {
			t.Fatalf("ramp %v options=%#v err=%v", percentage, stub.ramping, err)
		}
	}
	if err := RampVersion(context.Background(), stub, "presale-v2", "release-42", 10); err == nil {
		t.Fatal("unsupported percentage was accepted")
	}
	if err := RampVersion(context.Background(), stub, "", "release-42", 0); err != nil || stub.ramping.BuildID != "" {
		t.Fatalf("abort options=%#v err=%v", stub.ramping, err)
	}
}

func TestMetricsExposeWorkflowFailures(t *testing.T) {
	registry := NewMetricsRegistry()
	registry.WithTags(map[string]string{"workflow_type": "PresaleIntegration"}).Counter("temporal_workflow_failed").Inc(2)
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if body := response.Body.String(); !strings.Contains(body, `temporal_workflow_failed_total{workflow_type="PresaleIntegration"} 2`) {
		t.Fatalf("metrics body = %s", body)
	}
}
