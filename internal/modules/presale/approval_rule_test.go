package presale

import (
	"encoding/json"
	"errors"
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

func TestApprovalReplaySupportsThirdAndLaterApprovalNodes(t *testing.T) {
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "a1", Name: "销售", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "a2", Name: "技术", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
		{ID: "a3", Name: "复核", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := &ApprovalInstance{RequestID: 9, NodesJSON: nodes}
	actor := Actor{TenantID: "tenant-a", UserID: "sales", Roles: map[string]bool{"sales_director": true}}
	if next, ok := nextApprovalNode(instance, 2); !ok || next != 3 {
		t.Fatalf("next approval node=%d ok=%v, want 3", next, ok)
	}
	if got := approvalReplayAction(3, "PASS"); got != "NODE_3_PASS" {
		t.Fatalf("replay action=%s", got)
	}
	replay := &MutationReplay{RequestID: 9, ActorID: "sales", Operation: "APPROVAL_ACTION", Action: "NODE_3_PASS", RequestHash: "hash"}
	if err := validateApprovalReplay(replay, actor, 9, "PASS", "hash", instance); err != nil {
		t.Fatalf("third-node replay rejected: %v", err)
	}
	if err := validateApprovalReplay(replay, actor, 9, "REJECT", "hash", instance); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("wrong action replay err=%v", err)
	}
}
