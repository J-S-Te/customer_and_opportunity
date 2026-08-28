package temporalworker

import (
	"context"
	"fmt"
	"strings"

	"go.temporal.io/sdk/client"
)

// DeploymentHandle 是发布命令使用的最小 Temporal 控制面边界。
type DeploymentHandle interface {
	Describe(context.Context, client.WorkerDeploymentDescribeOptions) (client.WorkerDeploymentDescribeResponse, error)
	SetCurrentVersion(context.Context, client.WorkerDeploymentSetCurrentVersionOptions) (client.WorkerDeploymentSetCurrentVersionResponse, error)
	SetRampingVersion(context.Context, client.WorkerDeploymentSetRampingVersionOptions) (client.WorkerDeploymentSetRampingVersionResponse, error)
}

// PromoteCurrent 使用最新 conflict token 提升已有 Poller 的 Build ID。
func PromoteCurrent(ctx context.Context, handle DeploymentHandle, buildID, identity string) error {
	if handle == nil || strings.TrimSpace(buildID) == "" || strings.TrimSpace(identity) == "" {
		return fmt.Errorf("deployment handle, build ID and identity are required")
	}
	description, err := handle.Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		return fmt.Errorf("describe Temporal worker deployment: %w", err)
	}
	_, err = handle.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{
		BuildID: strings.TrimSpace(buildID), ConflictToken: description.ConflictToken,
		Identity: strings.TrimSpace(identity), IgnoreMissingTaskQueues: false, AllowNoPollers: false,
	})
	if err != nil {
		return fmt.Errorf("promote Temporal current worker version: %w", err)
	}
	return nil
}

// RampVersion 仅允许受控的 5/25/50/100 灰度步骤；0 用于撤销灰度。
func RampVersion(ctx context.Context, handle DeploymentHandle, buildID, identity string, percentage float32) error {
	if handle == nil || strings.TrimSpace(identity) == "" || !allowedPercentage(percentage) || percentage > 0 && strings.TrimSpace(buildID) == "" {
		return fmt.Errorf("valid deployment handle, build ID, identity and ramp percentage are required")
	}
	description, err := handle.Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		return fmt.Errorf("describe Temporal worker deployment: %w", err)
	}
	_, err = handle.SetRampingVersion(ctx, client.WorkerDeploymentSetRampingVersionOptions{
		BuildID: strings.TrimSpace(buildID), Percentage: percentage, ConflictToken: description.ConflictToken,
		Identity: strings.TrimSpace(identity), IgnoreMissingTaskQueues: false,
	})
	if err != nil {
		return fmt.Errorf("update Temporal ramping worker version: %w", err)
	}
	return nil
}

func allowedPercentage(value float32) bool {
	for _, allowed := range []float32{0, 5, 25, 50, 100} {
		if value == allowed {
			return true
		}
	}
	return false
}
