package presale

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ApprovalRule 是售前审批规则的租户级配置。申请启动时会复制命中的规则节点，
// 因此后续编辑规则不会改变已经提交的申请。
type ApprovalRule struct {
	ID         string             `json:"id"`
	TenantID   string             `json:"tenant_id"`
	Name       string             `json:"name"`
	Priority   int                `json:"priority"`
	Enabled    bool               `json:"enabled"`
	Expression ApprovalExpression `json:"expression"`
	Nodes      []ApprovalNode     `json:"nodes"`
	Version    uint64             `json:"version"`
}

type ApprovalNodeType string

const (
	ApprovalNodeApproval   ApprovalNodeType = "APPROVAL"
	ApprovalNodeDepartment ApprovalNodeType = "DEPARTMENT_ASSIGNMENT"
	// ApprovalNodePerson 是 TS-003 的一级指派节点，只为已提交的历史申请保留。
	ApprovalNodePerson   ApprovalNodeType = "PERSON_ASSIGNMENT"
	ApprovalNodeTeamLead ApprovalNodeType = "TEAM_LEAD_ASSIGNMENT"
	ApprovalNodeExecutor ApprovalNodeType = "EXECUTOR_ASSIGNMENT"
)

// IsAssignmentNodeType 区分审批节点与审批通过后的执行指派节点。
func IsAssignmentNodeType(value ApprovalNodeType) bool {
	switch value {
	case ApprovalNodeDepartment, ApprovalNodePerson, ApprovalNodeTeamLead, ApprovalNodeExecutor:
		return true
	default:
		return false
	}
}

// UsesTwoLevelAssignment 只对规则快照做结构判断。历史 PERSON_ASSIGNMENT 规则
// 继续走一级指派，新规则一旦出现 TEAM_LEAD/EXECUTOR 节点就必须完成两级。
func UsesTwoLevelAssignment(nodes []ApprovalNode) bool {
	for _, node := range nodes {
		if node.Type == ApprovalNodeTeamLead || node.Type == ApprovalNodeExecutor {
			return true
		}
	}
	return false
}

// AssignmentActionsForRole 返回某角色在规则快照中承担的全部指派动作。
// 技术总监可以同时承担“选部门”和“选团队负责人”，因此不能用单个动作判断权限。
func AssignmentActionsForRole(nodes []ApprovalNode, roleCode string) []ApprovalNodeType {
	roleCode = strings.TrimSpace(roleCode)
	if roleCode == "" {
		return nil
	}
	actions := make([]ApprovalNodeType, 0, 2)
	for _, node := range nodes {
		if strings.TrimSpace(node.RoleCode) != roleCode || !IsAssignmentNodeType(node.Type) {
			continue
		}
		actions = append(actions, node.Type)
	}
	return actions
}

func RoleHasAssignmentAction(nodes []ApprovalNode, roleCode string, action ApprovalNodeType) bool {
	for _, configured := range AssignmentActionsForRole(nodes, roleCode) {
		if configured == action {
			return true
		}
	}
	return false
}

// AssignmentActionForRole 返回角色配置的第一个执行动作。
// 保留该方法以兼容历史单层流程；双层调用方
// 必须使用 AssignmentActionsForRole，避免同时持有部门和
// 组长动作的角色被截断到第一个节点。
func AssignmentActionForRole(nodes []ApprovalNode, roleCode string) (ApprovalNodeType, bool) {
	actions := AssignmentActionsForRole(nodes, roleCode)
	if len(actions) == 0 {
		return "", false
	}
	return actions[0], true
}

type ApprovalNode struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Type        ApprovalNodeType `json:"type"`
	RoleCode    string           `json:"role_code"`
	Countersign string           `json:"countersign"`
}

type ApprovalExpression struct {
	Logical    string              `json:"logical,omitempty"`
	Conditions []ApprovalCondition `json:"conditions,omitempty"`
}

type ApprovalCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type ApprovalFacts struct {
	Urgency       string
	Venue         string
	ServiceHours  int64
	OpportunityID uint64
}

var ErrInvalidApprovalRule = errors.New("invalid presale approval rule")

// MatchHighest 与合同管理保持相同语义：优先级高者先匹配，命中后固化节点快照。
func MatchHighestApprovalRule(rules []ApprovalRule, facts ApprovalFacts) (*ApprovalRule, error) {
	candidates := append([]ApprovalRule(nil), rules...)
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Priority > candidates[j].Priority })
	for i := range candidates {
		if !candidates[i].Enabled {
			continue
		}
		matched, err := candidates[i].Expression.Match(facts)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", candidates[i].ID, err)
		}
		if matched {
			copy := candidates[i]
			copy.Nodes = append([]ApprovalNode(nil), candidates[i].Nodes...)
			return &copy, nil
		}
	}
	return nil, nil
}

func (e ApprovalExpression) Match(f ApprovalFacts) (bool, error) {
	if len(e.Conditions) == 0 {
		return false, fmt.Errorf("%w: empty conditions", ErrInvalidApprovalRule)
	}
	logical := strings.ToLower(strings.TrimSpace(e.Logical))
	if logical == "" {
		logical = "and"
	}
	if logical != "and" && logical != "or" {
		return false, fmt.Errorf("%w: logical operator", ErrInvalidApprovalRule)
	}
	matched := logical == "and"
	for _, c := range e.Conditions {
		value, err := c.match(f)
		if err != nil {
			return false, err
		}
		if logical == "and" {
			matched = matched && value
		} else {
			matched = matched || value
		}
	}
	return matched, nil
}

func (c ApprovalCondition) match(f ApprovalFacts) (bool, error) {
	var actual string
	switch c.Field {
	case "urgency":
		actual = f.Urgency
	case "venue":
		actual = f.Venue
	default:
		return false, fmt.Errorf("%w: unsupported field %s", ErrInvalidApprovalRule, c.Field)
	}
	value, ok := c.Value.(string)
	if !ok {
		return false, fmt.Errorf("%w: condition value", ErrInvalidApprovalRule)
	}
	switch strings.ToLower(c.Operator) {
	case "eq":
		return actual == value, nil
	case "ne":
		return actual != value, nil
	case "in":
		for _, item := range strings.Split(value, ",") {
			if strings.TrimSpace(item) == actual {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("%w: unsupported operator", ErrInvalidApprovalRule)
	}
}
