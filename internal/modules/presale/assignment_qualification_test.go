package presale

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
)

type listFailingDirectory struct{ *ts011Directory }

func (listFailingDirectory) List(context.Context, ownerdirectory.Query) (ownerdirectory.Page, error) {
	return ownerdirectory.Page{}, ownerdirectory.ErrUnavailable
}

func unqualifiedDirectoryUser(directory *ts011Directory) {
	directory.users["sales-x"] = ownerdirectory.User{
		ID: "sales-x", DisplayName: "无执行权限销售", Organizations: []ownerdirectory.Organization{ts011Organization("org-1", "交付一部")},
	}
	directory.roles["sales-x"] = "sales"
}

func TestSelectExecutionDepartmentRejectsUnqualifiedTeamLeadBeforePersistence(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}}
	directory := ts011NewDirectory()
	unqualifiedDirectoryUser(directory)
	notifications := &captureWorkflowNotifications{}
	service := newTS011Service(repo, directory).UseWorkflowNotifications(notifications)

	_, err := service.SelectExecutionDepartment(context.Background(), ts011TechnicalDirector(), 9, "unqualified-lead", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "sales-x", Version: 3,
	})
	if !errors.Is(err, ErrAssigneeNotQualified) {
		t.Fatalf("error=%v, want assignee not qualified", err)
	}
	if repo.request.Status != StatusApprovedPendingAssignment || repo.request.TeamLeadPersonID != "" || repo.assignmentAdds != 0 || repo.requestUpdates != 0 {
		t.Fatalf("unqualified team lead reached persistence: request=%+v adds=%d updates=%d", repo.request, repo.assignmentAdds, repo.requestUpdates)
	}
	if len(notifications.written) != 0 || len(repo.outbox) != 0 {
		t.Fatalf("unqualified team lead produced notifications: notifications=%+v outbox=%+v", notifications.written, repo.outbox)
	}

	appErr := apperror.As(apiError(err))
	if appErr.HTTPStatus != 422 || appErr.Code != "CRM_PRESALE_ASSIGNEE_NOT_QUALIFIED" {
		t.Fatalf("api error status=%d code=%s", appErr.HTTPStatus, appErr.Code)
	}
	payload, marshalErr := json.Marshal(appErr.Details)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if !strings.Contains(string(payload), "sales-x") || !strings.Contains(string(payload), "presale.assign") {
		t.Fatalf("error details are not actionable: %s", payload)
	}
}

func TestReplaceExecutorAssignmentsRejectsUnqualifiedAssigneeBeforePersistence(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	request.Status, request.TeamLeadPersonID, request.AssignmentLevel = StatusPendingExecutionAssignment, "lead-a", AssignmentLevelTeamLead
	repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}, assignments: []Assignment{
		{BaseModel: BaseModel{ID: 1, TenantID: "tenant-a", Version: 1}, RequestID: 9, AssigneeID: "lead-a", AssigneeNameSnapshot: "团队负责人甲", AssigneeRole: "team_lead", AssignmentKind: AssignmentKindTeamLead, IsCurrent: true, BatchNo: 1},
	}}}
	directory := ts011NewDirectory()
	unqualifiedDirectoryUser(directory)
	notifications := &captureWorkflowNotifications{}
	service := newTS011Service(repo, directory).UseWorkflowNotifications(notifications)

	_, err := service.ReplaceExecutorAssignments(context.Background(), ts011TeamLead("lead-a"), 9, "unqualified-executor", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "sales-x", Role: "technician"}}, ChangeReason: "新增执行人", Version: 3,
	})
	if !errors.Is(err, ErrAssigneeNotQualified) {
		t.Fatalf("error=%v, want assignee not qualified", err)
	}
	if repo.assignmentAdds != 0 || repo.requestUpdates != 0 || len(repo.events) != 0 || len(repo.outbox) != 0 || len(notifications.written) != 0 {
		t.Fatalf("unqualified assignee reached persistence: adds=%d updates=%d events=%d outbox=%d notifications=%d",
			repo.assignmentAdds, repo.requestUpdates, len(repo.events), len(repo.outbox), len(notifications.written))
	}
	if repo.request.Status != StatusPendingExecutionAssignment {
		t.Fatalf("request status changed to %s", repo.request.Status)
	}
}

func TestAssignmentQualificationFailsClosedWhenDirectoryCannotBeVerified(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}}
	directory := listFailingDirectory{ts011NewDirectory()}
	service := newTS011Service(repo, directory)

	_, err := service.SelectExecutionDepartment(context.Background(), ts011TechnicalDirector(), 9, "unverifiable-lead", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "lead-a", Version: 3,
	})
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("error=%v, want dependency unavailable", err)
	}
	if repo.assignmentAdds != 0 || repo.requestUpdates != 0 || repo.request.Status != StatusApprovedPendingAssignment {
		t.Fatalf("unverifiable qualification reached persistence: adds=%d updates=%d status=%s", repo.assignmentAdds, repo.requestUpdates, repo.request.Status)
	}
}

func TestLegacyAssignmentExactReplaySurvivesLaterRoleRevocation(t *testing.T) {
	request, instance := ts011Request(t, ts011LegacyNodes(t))
	repo := &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}
	directory := ts011NewDirectory()
	service := NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}, fixedIDs{}).UseOwnerDirectory(directory)
	input := ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "历史规则指派", Version: 3,
	}
	if _, err := service.ReplaceAssignments(context.Background(), ts011TechnicalDirector(), 9, "legacy-replay", input); err != nil {
		t.Fatalf("first legacy assignment: %v", err)
	}
	// 精确重放不应因为人员此后被撤销角色而把已提交成功改写成 422。
	delete(directory.roles, "exec-a")
	if _, err := service.ReplaceAssignments(context.Background(), ts011TechnicalDirector(), 9, "legacy-replay", input); err != nil {
		t.Fatalf("exact legacy replay after role revocation: %v", err)
	}
	if repo.assignmentAdds != 1 {
		t.Fatalf("assignments added=%d, want 1", repo.assignmentAdds)
	}
}

func TestTeamLeadExactReplaySurvivesLaterRoleRevocation(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}}
	directory := ts011NewDirectory()
	service := newTS011Service(repo, directory)
	input := SelectDepartmentInput{DepartmentID: "org-1", TeamLeadPersonID: "lead-a", Version: 3}
	if _, err := service.SelectExecutionDepartment(context.Background(), ts011TechnicalDirector(), 9, "lead-replay", input); err != nil {
		t.Fatalf("first team lead assignment: %v", err)
	}
	// 精确重放应先返回已提交结果；负责人此后被撤掉 team_lead 角色不能改写成功响应。
	directory.roles["lead-a"] = "sales"
	if _, err := service.SelectExecutionDepartment(context.Background(), ts011TechnicalDirector(), 9, "lead-replay", input); err != nil {
		t.Fatalf("exact team lead replay after role revocation: %v", err)
	}
	if repo.assignmentAdds != 1 {
		t.Fatalf("assignments added=%d, want 1", repo.assignmentAdds)
	}
}

func TestAssignmentDutyPermissionMappingComesFromAuthorizationCatalog(t *testing.T) {
	executorRoles := assignmentDutyRoleCodes(AssignmentDutyExecutor)
	for _, role := range []string{"technician", "project_manager", "team_lead"} {
		if !qualificationContains(executorRoles, role) {
			t.Fatalf("executor duty roles=%v missing %s", executorRoles, role)
		}
	}
	if qualificationContains(executorRoles, "sales") || qualificationContains(executorRoles, "technical_director") {
		t.Fatalf("executor duty roles incorrectly include non-executing roles: %v", executorRoles)
	}
	leadRoles := assignmentDutyRoleCodes(AssignmentDutyTeamLead)
	for _, role := range []string{"technical_director", "team_lead", "crm_super_admin"} {
		if !qualificationContains(leadRoles, role) {
			t.Fatalf("team lead duty roles=%v missing %s", leadRoles, role)
		}
	}
	if qualificationContains(leadRoles, "technician") || qualificationContains(leadRoles, "sales") {
		t.Fatalf("team lead duty roles incorrectly include non-assigning roles: %v", leadRoles)
	}
}

func qualificationContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
