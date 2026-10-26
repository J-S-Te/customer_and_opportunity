package presale

import (
	"encoding/json"
	"sort"
	"strings"
)

func normalizeAssignmentTargets(assignees []AssignmentTarget) (map[string]AssignmentTarget, []string, []AssignmentTarget, error) {
	if len(assignees) == 0 || len(assignees) > 100 {
		return nil, nil, nil, ErrInvalidInput
	}
	targets := map[string]AssignmentTarget{}
	// 执行岗位明确区分项目经理与技术人员。implementation_engineer 仅保留给历史指派记录，
	// 新指派统一使用 technician，不能再把负责人或总监作为执行人员写入。
	allowedRoles := map[string]bool{"team_lead": true, "project_manager": true, "technician": true}
	for _, v := range assignees {
		if strings.TrimSpace(v.PersonID) == "" || !allowedRoles[v.Role] || len([]rune(v.PersonName)) > 128 || len([]rune(v.Department)) > 128 || len([]rune(v.DepartmentID)) > 64 {
			return nil, nil, nil, ErrInvalidInput
		}
		v.PersonID = strings.TrimSpace(v.PersonID)
		v.PersonName = strings.TrimSpace(v.PersonName)
		v.Department = strings.TrimSpace(v.Department)
		v.DepartmentID = strings.TrimSpace(v.DepartmentID)
		v.Role = strings.TrimSpace(v.Role)
		targets[v.PersonID] = v
	}
	if len(targets) != len(assignees) {
		return nil, nil, nil, ErrInvalidInput
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	canonical := make([]AssignmentTarget, 0, len(ids))
	for _, id := range ids {
		canonical = append(canonical, targets[id])
	}
	return targets, ids, canonical, nil
}

// 姓名和组织名称只是展示快照；客户端明确选择的组织 ID 仍属于请求意图。

// 姓名和组织名称只是展示快照；客户端明确选择的组织 ID 仍属于请求意图。
func assignmentIntentTargets(targets []AssignmentTarget) []AssignmentTarget {
	intent := make([]AssignmentTarget, 0, len(targets))
	for _, target := range targets {
		intent = append(intent, AssignmentTarget{PersonID: target.PersonID, Role: target.Role, DepartmentID: target.DepartmentID})
	}
	return intent
}

func currentAssignment(assignments []Assignment, personID string) (Assignment, bool) {
	for _, assignment := range assignments {
		if assignment.AssigneeID == personID {
			return assignment, true
		}
	}
	return Assignment{}, false
}

// assignmentActionAllowed 是历史单层指派动作的唯一运行时
// 门禁。审批规则以快照形式保存在实例上，因此
// 规则变化只影响新请求，不能在执行中途改变
// 有效工作流。
func assignmentActionAllowed(actor Actor, instance *ApprovalInstance, action ApprovalNodeType) bool {
	if actor.HasRole("crm_super_admin") {
		return true
	}
	// 对规则快照可用前创建的请求，保留
	// 旧版行为。
	if instance == nil || len(instance.NodesJSON) == 0 {
		return (action == ApprovalNodeDepartment && actor.HasRole("technical_director")) ||
			(action == ApprovalNodePerson && (actor.HasRole("technical_director") || actor.HasRole("team_lead")))
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil {
		return false
	}
	for roleCode := range actor.Roles {
		if RoleHasAssignmentAction(nodes, roleCode, action) {
			return true
		}
	}
	return false
}

// assignmentActionsAllowed 要求同一个角色同时承担全部动作。TS-011 的第①级
// 是一个“部门 + 团队负责人”的原子操作，不能由两个不同角色各持一半后越权拼接。

// assignmentActionsAllowed 要求同一个角色同时承担全部动作。TS-011 的第①级
// 是一个“部门 + 团队负责人”的原子操作，不能由两个不同角色各持一半后越权拼接。
func assignmentActionsAllowed(actor Actor, instance *ApprovalInstance, actions ...ApprovalNodeType) bool {
	if len(actions) == 0 {
		return false
	}
	if actor.HasRole("crm_super_admin") {
		return true
	}
	if instance == nil || len(instance.NodesJSON) == 0 {
		for _, action := range actions {
			switch action {
			case ApprovalNodeDepartment:
				if !actor.HasRole("technical_director") {
					return false
				}
			case ApprovalNodePerson:
				if !actor.HasRole("technical_director") && !actor.HasRole("team_lead") {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil {
		return false
	}
	for roleCode := range actor.Roles {
		allowed := true
		for _, action := range actions {
			if !RoleHasAssignmentAction(nodes, roleCode, action) {
				allowed = false
				break
			}
		}
		if allowed {
			return true
		}
	}
	return false
}

// instanceUsesTwoLevelAssignment 只读取申请启动时固化的节点快照。
// 新规则上线后编辑规则不会把在途旧申请切到两级流程。

// instanceUsesTwoLevelAssignment 只读取申请启动时固化的节点快照。
// 新规则上线后编辑规则不会把在途旧申请切到两级流程。
func instanceUsesTwoLevelAssignment(instance *ApprovalInstance) bool {
	if instance == nil || len(instance.NodesJSON) == 0 {
		return false
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil {
		return false
	}
	return UsesTwoLevelAssignment(nodes)
}
