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
	ApprovalNodePerson     ApprovalNodeType = "PERSON_ASSIGNMENT"
)

type ApprovalNode struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Type        ApprovalNodeType `json:"type"`
	RoleCode    string           `json:"role_code"`
	Countersign string           `json:"countersign"`
}

// AssignmentActionForRole returns the configured execution action for a role.
// Approval nodes decide who approves; assignment nodes decide which execution
// control the role receives after approval.
func AssignmentActionForRole(nodes []ApprovalNode, roleCode string) (ApprovalNodeType, bool) {
	roleCode = strings.TrimSpace(roleCode)
	if roleCode == "" {
		return "", false
	}
	for _, node := range nodes {
		if strings.TrimSpace(node.RoleCode) != roleCode {
			continue
		}
		if node.Type == ApprovalNodeDepartment || node.Type == ApprovalNodePerson {
			return node.Type, true
		}
	}
	return "", false
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
