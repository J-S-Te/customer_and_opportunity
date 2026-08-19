package presale

import (
	"encoding/json"
	"testing"
)

func TestMatchHighestApprovalRuleUsesPriorityAndCopiesNodes(t *testing.T) {
	rules := []ApprovalRule{
		{ID: "low", Priority: 1, Enabled: true, Expression: ApprovalExpression{Conditions: []ApprovalCondition{{Field: "urgency", Operator: "eq", Value: "NORMAL"}}}, Nodes: []ApprovalNode{{ID: "sales", Type: ApprovalNodeApproval, RoleCode: "sales_director"}}},
		{ID: "high", Priority: 2, Enabled: true, Expression: ApprovalExpression{Conditions: []ApprovalCondition{{Field: "urgency", Operator: "eq", Value: "NORMAL"}}}, Nodes: []ApprovalNode{{ID: "tech", Type: ApprovalNodeApproval, RoleCode: "technical_director"}}},
	}
	matched, err := MatchHighestApprovalRule(rules, ApprovalFacts{Urgency: "NORMAL"})
	if err != nil || matched == nil || matched.ID != "high" {
		t.Fatalf("matched=%+v err=%v", matched, err)
	}
	rules[1].Nodes[0].RoleCode = "tampered"
	if matched.Nodes[0].RoleCode != "technical_director" {
		t.Fatalf("rule nodes were not snapshotted")
	}
}

func TestApprovalExpressionSupportsVenueAndUrgency(t *testing.T) {
	expression := ApprovalExpression{Logical: "and", Conditions: []ApprovalCondition{{Field: "urgency", Operator: "eq", Value: "URGENT"}, {Field: "venue", Operator: "in", Value: "ONSITE,REMOTE"}}}
	matched, err := expression.Match(ApprovalFacts{Urgency: "URGENT", Venue: "ONSITE"})
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
}

func TestNextApprovalNodeSkipsAssignmentNodes(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "sales", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "technical", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
		{ID: "department", Type: ApprovalNodeDepartment, RoleCode: "technical_director"},
		{ID: "person", Type: ApprovalNodePerson, RoleCode: "team_lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := &ApprovalInstance{NodesJSON: nodes}
	if next, ok := nextApprovalNode(instance, 1); !ok || next != 2 {
		t.Fatalf("next approval after node 1 = %d, %v", next, ok)
	}
	if next, ok := nextApprovalNode(instance, 2); ok || next != 0 {
		t.Fatalf("assignment nodes must not keep approval pending: next=%d ok=%v", next, ok)
	}
}

func TestAssignmentActionForRoleUsesAssignmentNode(t *testing.T) {
	nodes := []ApprovalNode{
		{Type: ApprovalNodeApproval, RoleCode: "technical_director"},
		{Type: ApprovalNodePerson, RoleCode: "technical_director"},
		{Type: ApprovalNodeDepartment, RoleCode: "team_lead"},
	}
	if action, ok := AssignmentActionForRole(nodes, "technical_director"); !ok || action != ApprovalNodePerson {
		t.Fatalf("technical director action = %q, %v; want PERSON_ASSIGNMENT", action, ok)
	}
	if action, ok := AssignmentActionForRole(nodes, "sales_director"); ok || action != "" {
		t.Fatalf("unexpected action for unconfigured role: %q, %v", action, ok)
	}
}

func TestAssignmentActionAllowedFollowsRuleSnapshot(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{{Type: ApprovalNodePerson, RoleCode: "technical_director"}})
	if err != nil {
		t.Fatal(err)
	}
	instance := &ApprovalInstance{NodesJSON: nodes}
	actor := Actor{Roles: map[string]bool{"technical_director": true}}
	if !assignmentActionAllowed(actor, instance, ApprovalNodePerson) {
		t.Fatal("technical director should be allowed to select people")
	}
	if assignmentActionAllowed(actor, instance, ApprovalNodeDepartment) {
		t.Fatal("technical director should not be allowed to select a department")
	}
}
