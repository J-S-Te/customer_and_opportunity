// Package capability 定义按客户生效的门户服务项。
//
// 短期模型：portal_customer 角色保持不变，权限判定为“角色权限 ∩ 客户服务项”。
// 服务项按 tenant+customer 存储，缺失行视为全部开通，保证存量客户升级后能力不变。
package capability

import "strings"

const (
	ProjectEnabled    = "project_enabled"
	ReportEnabled     = "report_enabled"
	FilingEnabled     = "filing_enabled"
	FeedbackEnabled   = "feedback_enabled"
	EvaluationEnabled = "evaluation_enabled"
)

// AllKeys 是当前支持的全部服务项，配置接口只接受这些键。
var AllKeys = []string{ProjectEnabled, ReportEnabled, FilingEnabled, FeedbackEnabled, EvaluationEnabled}

// Options 是某个客户当前开通的门户服务项。
type Options struct {
	ProjectEnabled    bool `json:"project_enabled"`
	ReportEnabled     bool `json:"report_enabled"`
	FilingEnabled     bool `json:"filing_enabled"`
	FeedbackEnabled   bool `json:"feedback_enabled"`
	EvaluationEnabled bool `json:"evaluation_enabled"`
}

// DefaultOptions 返回全部开通；缺失配置行与历史客户使用该默认值。
func DefaultOptions() Options {
	return Options{ProjectEnabled: true, ReportEnabled: true, FilingEnabled: true, FeedbackEnabled: true, EvaluationEnabled: true}
}

// ToMap 返回全部服务项键值，便于 JSON 持久化与机器接口传输。
func (o Options) ToMap() map[string]bool {
	return map[string]bool{
		ProjectEnabled:    o.ProjectEnabled,
		ReportEnabled:     o.ReportEnabled,
		FilingEnabled:     o.FilingEnabled,
		FeedbackEnabled:   o.FeedbackEnabled,
		EvaluationEnabled: o.EvaluationEnabled,
	}
}

// OptionsFromMap 从配置字典还原服务项；未知键忽略，缺失键保持默认开通。
func OptionsFromMap(values map[string]bool) Options {
	options := DefaultOptions()
	if values == nil {
		return options
	}
	if value, ok := values[ProjectEnabled]; ok {
		options.ProjectEnabled = value
	}
	if value, ok := values[ReportEnabled]; ok {
		options.ReportEnabled = value
	}
	if value, ok := values[FilingEnabled]; ok {
		options.FilingEnabled = value
	}
	if value, ok := values[FeedbackEnabled]; ok {
		options.FeedbackEnabled = value
	}
	if value, ok := values[EvaluationEnabled]; ok {
		options.EvaluationEnabled = value
	}
	return options
}

// PermissionService 返回权限所属的服务项；不属于任何可配置服务的权限（如账号安全）恒不受开关影响。
func PermissionService(permission string) string {
	switch {
	case strings.HasPrefix(permission, "project."):
		return ProjectEnabled
	case strings.HasPrefix(permission, "report."):
		return ReportEnabled
	case strings.HasPrefix(permission, "filing."):
		return FilingEnabled
	case strings.HasPrefix(permission, "feedback."):
		return FeedbackEnabled
	case strings.HasPrefix(permission, "evaluation."):
		return EvaluationEnabled
	default:
		return ""
	}
}

// IntersectPermissions 按客户服务项过滤权限；未归类权限保持原样（不参与开关控制）。
func IntersectPermissions(permissions []string, options Options) []string {
	enabled := options.ToMap()
	result := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		service := PermissionService(permission)
		if service == "" || enabled[service] {
			result = append(result, permission)
		}
	}
	return result
}
