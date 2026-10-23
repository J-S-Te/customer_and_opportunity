package presale

import (
	"context"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

// 指派职责来自申请上的指派类型（负责人/执行人），而不是客户端提交的业务岗位名称；
// 角色到权限的映射始终从平台目录（platformcatalog）读取，不在业务代码里硬编码角色名。
const (
	AssignmentDutyTeamLead = ownerdirectory.AssignmentDutyTeamLead
	AssignmentDutyExecutor = ownerdirectory.AssignmentDutyExecutor
)

// assignmentDutyPermissions 描述每种指派职责实际需要的现有权限：
//   - 团队负责人必须能继续选择执行人（presale.assign）；
//   - 执行人必须具备登记进展、登记工时和结束任务所需的权限。
//
// 这些权限码与服务端 AddProgress/AddWorklog/Complete/ReplaceExecutorAssignments
// 的现有门禁保持一致，不额外发明新的权限。
var assignmentDutyPermissions = map[string][]string{
	AssignmentDutyTeamLead: ownerdirectory.AssignmentDutyPermissions(AssignmentDutyTeamLead),
	AssignmentDutyExecutor: ownerdirectory.AssignmentDutyPermissions(AssignmentDutyExecutor),
}

// assigneeNotQualifiedError 保留接收人和缺少的权限，供 API 返回可执行提示；
// 不自动补授权，也不创建指派或通知。
type assigneeNotQualifiedError struct {
	AssigneeID          string
	RequiredPermissions []string
}

func (e *assigneeNotQualifiedError) Error() string { return ErrAssigneeNotQualified.Error() }

func (e *assigneeNotQualifiedError) Unwrap() error { return ErrAssigneeNotQualified }

// 平台负责人目录只暴露角色绑定，不提供任意用户的权限集合；CRM 的 Token 权限与
// 这份已发布授权目录做精确比对，因此“用户持有目录内某角色”等价于“用户具备该
// 角色在目录中的全部权限”。
//
// assignmentDutyRoleCodesByDuty 在启动时从同一份 CRM 授权目录计算“完整具备该职责”
// 的角色集合。一个角色必须覆盖职责所需的全部权限，避免把部分权限误当成履职资格。
var assignmentDutyRoleCodesByDuty = buildAssignmentDutyRoleCodes()

func buildAssignmentDutyRoleCodes() map[string][]string {
	result := make(map[string][]string, len(assignmentDutyPermissions))
	for duty := range assignmentDutyPermissions {
		result[duty] = ownerdirectory.AssignmentDutyRoleCodes(duty)
	}
	return result
}

func assignmentDutyRoleCodes(duty string) []string {
	return append([]string(nil), assignmentDutyRoleCodesByDuty[duty]...)
}

// ensureAssignmentDutyEligible 在写入指派或发送通知之前核验接收人当前是否
// 已具备该职责所需权限。目录不可用或职责无法映射时失败关闭，不猜测放行。
func (s *Service) ensureAssignmentDutyEligible(ctx context.Context, duty string, targets []AssignmentTarget) error {
	if len(targets) == 0 {
		return nil
	}
	if s.ownerDirectory == nil {
		return ErrDependencyUnavailable
	}
	required, ok := assignmentDutyPermissions[duty]
	if !ok {
		return ErrInvalidInput
	}
	roleCodes := assignmentDutyRoleCodes(duty)
	if len(roleCodes) == 0 {
		return ErrDependencyUnavailable
	}
	checked := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		personID := strings.TrimSpace(target.PersonID)
		if personID == "" {
			return ErrInvalidInput
		}
		if _, exists := checked[personID]; exists {
			continue
		}
		checked[personID] = struct{}{}
		qualified, err := s.directoryUserHoldsAnyRole(ctx, personID, roleCodes)
		if err != nil {
			return ErrDependencyUnavailable
		}
		if !qualified {
			return &assigneeNotQualifiedError{AssigneeID: personID, RequiredPermissions: append([]string(nil), required...)}
		}
	}
	return nil
}

// directoryUserHoldsAnyRole 按用户精确查询，结果只用于资格判断，不作为业务授权快照。
func (s *Service) directoryUserHoldsAnyRole(ctx context.Context, userID string, roleCodes []string) (bool, error) {
	userID = strings.TrimSpace(userID)
	for pageNumber := 1; pageNumber <= ownerDirectoryMaxPages; pageNumber++ {
		page, err := s.ownerDirectory.List(ctx, ownerdirectory.Query{
			UserID: userID, RoleCodes: roleCodes, Page: pageNumber, PageSize: ownerDirectoryPageSize,
		})
		if err != nil {
			return false, err
		}
		for _, user := range page.Items {
			if strings.TrimSpace(user.ID) == userID {
				return true, nil
			}
		}
		if len(page.Items) == 0 || int64(pageNumber*ownerDirectoryPageSize) >= page.Total || len(page.Items) < ownerDirectoryPageSize {
			return false, nil
		}
	}
	return false, ownerdirectory.ErrUnavailable
}
