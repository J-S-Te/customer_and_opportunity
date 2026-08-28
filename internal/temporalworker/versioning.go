// Package temporalworker 封装客户与商机子系统自己的 Temporal Worker 发布边界。
package temporalworker

import (
	"fmt"
	"strings"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// VersioningConfig 描述售前 Worker 的 Deployment 版本与默认工作流升级策略。
type VersioningConfig struct {
	Enabled        bool
	DeploymentName string
	BuildID        string
	Policy         string
}

// WorkerOptions 生成售前 Temporal Worker 的 Deployment-based Versioning 选项。
func WorkerOptions(config VersioningConfig) (worker.Options, error) {
	buildID := strings.TrimSpace(config.BuildID)
	if buildID == "" {
		return worker.Options{}, fmt.Errorf("Temporal worker build ID must not be empty")
	}
	options := worker.Options{DisableRegistrationAliasing: true, BuildID: buildID}
	if !config.Enabled {
		return options, nil
	}
	deployment := strings.TrimSpace(config.DeploymentName)
	if deployment == "" {
		return worker.Options{}, fmt.Errorf("Temporal worker deployment name must not be empty")
	}
	behavior, err := versioningBehavior(config.Policy)
	if err != nil {
		return worker.Options{}, err
	}
	options.DeploymentOptions = worker.DeploymentOptions{
		UseVersioning:             true,
		Version:                   worker.WorkerDeploymentVersion{DeploymentName: deployment, BuildID: buildID},
		DefaultVersioningBehavior: behavior,
	}
	return options, nil
}

func versioningBehavior(policy string) (workflow.VersioningBehavior, error) {
	switch strings.ToUpper(strings.TrimSpace(policy)) {
	case "PINNED":
		return workflow.VersioningBehaviorPinned, nil
	case "AUTO_UPGRADE":
		return workflow.VersioningBehaviorAutoUpgrade, nil
	default:
		return workflow.VersioningBehaviorUnspecified, fmt.Errorf("unsupported Temporal worker versioning policy %q", policy)
	}
}
