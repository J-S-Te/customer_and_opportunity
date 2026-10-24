package presale

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

// ts011Repository 在既有 mutationRepository 上补齐两级指派所需的真实状态投影。
// 故意只模拟与状态机相关的持久化行为，避免测试把仓储实现细节当成业务事实。
type ts011Repository struct {
	*mutationRepository
	events     []AssignmentEvent
	outbox     []OutboxEvent
	statusLogs []StatusLog
}

func (r *ts011Repository) FindRequest(_ context.Context, tenant string, id uint64) (*PresaleRequest, error) {
	if r.request == nil || r.request.TenantID != tenant || r.request.ID != id {
		return nil, ErrNotFound
	}
	value := *r.request
	return &value, nil
}

func (r *ts011Repository) FindApprovalInstance(_ context.Context, tenant string, requestID uint64) (*ApprovalInstance, error) {
	if r.approval == nil || r.approval.RequestID != requestID {
		return nil, ErrNotFound
	}
	value := *r.approval
	return &value, nil
}

func (r *ts011Repository) UpdateRequestVersioned(_ context.Context, value *PresaleRequest, version uint64, fields map[string]any) error {
	if value.Version != version {
		return ErrVersionConflict
	}
	if r.request == nil {
		return ErrNotFound
	}
	applyTS011RequestFields(r.request, fields)
	r.request.Version = version + 1
	r.requestUpdates++
	value.Version = r.request.Version
	return nil
}

func applyTS011RequestFields(request *PresaleRequest, fields map[string]any) {
	if value, ok := fields["status"].(RequestStatus); ok {
		request.Status = value
	}
	if value, ok := fields["team_lead_person_id"].(string); ok {
		request.TeamLeadPersonID = value
	}
	if value, ok := fields["team_lead_name_snapshot"].(string); ok {
		request.TeamLeadNameSnapshot = value
	}
	if value, ok := fields["assignment_level"].(uint8); ok {
		request.AssignmentLevel = value
	}
	if value, ok := fields["execution_department_id"].(string); ok {
		request.ExecutionDepartmentID = value
	}
	if value, ok := fields["execution_department"].(string); ok {
		request.ExecutionDepartment = value
	}
}

func (r *ts011Repository) ListAssignmentsForUpdate(_ context.Context, tenant string, requestID uint64) ([]Assignment, error) {
	values := make([]Assignment, 0, len(r.assignments))
	for _, assignment := range r.assignments {
		if assignment.TenantID == tenant && assignment.RequestID == requestID {
			values = append(values, assignment)
		}
	}
	return values, nil
}

func (r *ts011Repository) ListCurrentAssignmentsForUpdate(_ context.Context, tenant string, requestID uint64) ([]Assignment, error) {
	values := make([]Assignment, 0, len(r.assignments))
	for _, assignment := range r.assignments {
		if assignment.TenantID != tenant || assignment.RequestID != requestID || !assignment.IsCurrent {
			continue
		}
		if assignment.AssignmentKind == AssignmentKindTeamLead {
			continue
		}
		values = append(values, assignment)
	}
	return values, nil
}

func (r *ts011Repository) ListAssignments(_ context.Context, tenant string, requestID uint64) ([]Assignment, error) {
	values := make([]Assignment, 0, len(r.assignments))
	for _, assignment := range r.assignments {
		if assignment.TenantID == tenant && assignment.RequestID == requestID {
			values = append(values, assignment)
		}
	}
	return values, nil
}

func (r *ts011Repository) ListCurrentAssignments(_ context.Context, tenant string, requestIDs []uint64) (map[uint64][]Assignment, error) {
	result := make(map[uint64][]Assignment)
	for _, requestID := range requestIDs {
		for _, assignment := range r.assignments {
			if assignment.TenantID != tenant || assignment.RequestID != requestID || !assignment.IsCurrent {
				continue
			}
			if assignment.AssignmentKind == AssignmentKindTeamLead {
				continue
			}
			result[requestID] = append(result[requestID], assignment)
		}
	}
	return result, nil
}

func (r *ts011Repository) RequestAggregate(context.Context, string, uint64) (RequestAggregate, error) {
	return RequestAggregate{TotalWorkHours: "0.00"}, nil
}

func (r *ts011Repository) AlertAggregate(context.Context, string, uint64) (AlertAggregate, error) {
	return AlertAggregate{Level: "NONE"}, nil
}

func (r *ts011Repository) EndAssignment(_ context.Context, tenant string, id, version uint64, by string, at time.Time) error {
	for index := range r.assignments {
		assignment := &r.assignments[index]
		if assignment.ID != id || assignment.TenantID != tenant || !assignment.IsCurrent {
			continue
		}
		if assignment.Version != version {
			return ErrVersionConflict
		}
		assignment.IsCurrent = false
		assignment.EndedAt = &at
		assignment.UpdatedBy = by
		assignment.Version++
		return nil
	}
	return ErrVersionConflict
}

func (r *ts011Repository) CreateAssignment(_ context.Context, value *Assignment) error {
	r.assignmentAdds++
	value.ID = uint64(100 + r.assignmentAdds)
	r.assignments = append(r.assignments, *value)
	return nil
}

func (r *ts011Repository) CreateAssignmentEvent(_ context.Context, value *AssignmentEvent) error {
	value.ID = uint64(len(r.events) + 1)
	r.events = append(r.events, *value)
	return nil
}

func (r *ts011Repository) CreateOutbox(_ context.Context, value *OutboxEvent) error {
	r.outbox = append(r.outbox, *value)
	r.outboxCount++
	return nil
}

func (r *ts011Repository) CreateStatusLog(_ context.Context, value *StatusLog) error {
	r.statusLogs = append(r.statusLogs, *value)
	r.statusCount++
	return nil
}

type ts011Directory struct {
	users map[string]ownerdirectory.User
	roles map[string]string
	err   error
}

func (d *ts011Directory) List(_ context.Context, query ownerdirectory.Query) (ownerdirectory.Page, error) {
	if d.err != nil {
		return ownerdirectory.Page{}, d.err
	}
	items := make([]ownerdirectory.User, 0, len(d.users))
	for userID, user := range d.users {
		if len(query.RoleCodes) > 0 {
			matched := false
			for _, role := range query.RoleCodes {
				if d.roles[userID] == role {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		items = append(items, user)
	}
	return ownerdirectory.Page{Items: items, Page: 1, PageSize: 50, Total: int64(len(items))}, nil
}

func (d *ts011Directory) Validate(_ context.Context, userID, organizationID string) error {
	if d.err != nil {
		return d.err
	}
	user, ok := d.users[userID]
	if !ok {
		return ownerdirectory.ErrSelectionInvalid
	}
	for _, organization := range user.Organizations {
		if organization.ID == organizationID {
			return nil
		}
	}
	return ownerdirectory.ErrSelectionInvalid
}

func (d *ts011Directory) Resolve(_ context.Context, userIDs []string) (map[string]ownerdirectory.User, error) {
	if d.err != nil {
		return nil, d.err
	}
	result := make(map[string]ownerdirectory.User, len(userIDs))
	for _, userID := range userIDs {
		if user, ok := d.users[userID]; ok {
			result[userID] = user
		}
	}
	return result, nil
}

func ts011Organization(id, name string) ownerdirectory.Organization {
	return ownerdirectory.Organization{ID: id, Name: name, IsPrimary: true}
}

func ts011NewDirectory() *ts011Directory {
	return &ts011Directory{
		users: map[string]ownerdirectory.User{
			"technical-director": {ID: "technical-director", DisplayName: "技术总监", Organizations: []ownerdirectory.Organization{ts011Organization("org-1", "技术中心")}},
			"lead-a":             {ID: "lead-a", DisplayName: "团队负责人甲", Organizations: []ownerdirectory.Organization{ts011Organization("org-1", "交付一部")}},
			"lead-b":             {ID: "lead-b", DisplayName: "团队负责人乙", Organizations: []ownerdirectory.Organization{ts011Organization("org-1", "交付一部")}},
			"exec-a":             {ID: "exec-a", DisplayName: "项目经理甲", Organizations: []ownerdirectory.Organization{ts011Organization("org-1", "交付一部")}},
			"exec-b":             {ID: "exec-b", DisplayName: "实施工程师乙", Organizations: []ownerdirectory.Organization{ts011Organization("org-1", "交付一部")}},
		},
		roles: map[string]string{
			"technical-director": "technical_director",
			"lead-a":             "team_lead",
			"lead-b":             "team_lead",
			"exec-a":             "project_manager",
			"exec-b":             "technician",
		},
	}
}

func ts011TwoLevelNodes(t *testing.T) []byte {
	t.Helper()
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "approval-1", Name: "销售审批", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "approval-2", Name: "技术审批", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
		{ID: "department", Name: "执行部门", Type: ApprovalNodeDepartment, RoleCode: "technical_director"},
		{ID: "team-lead", Name: "团队负责人", Type: ApprovalNodeTeamLead, RoleCode: "technical_director"},
		{ID: "executor", Name: "执行人", Type: ApprovalNodeExecutor, RoleCode: "team_lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func ts011LegacyNodes(t *testing.T) []byte {
	t.Helper()
	nodes, err := json.Marshal([]ApprovalNode{
		{ID: "approval-1", Name: "销售审批", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
		{ID: "approval-2", Name: "技术审批", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
		{ID: "department", Name: "执行部门", Type: ApprovalNodeDepartment, RoleCode: "technical_director"},
		{ID: "person", Name: "执行人", Type: ApprovalNodePerson, RoleCode: "technical_director"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func ts011TechnicalDirector() Actor {
	return Actor{
		TenantID: "tenant-a", UserID: "technical-director", PersonID: "technical-director",
		Roles: map[string]bool{"technical_director": true},
		Permissions: map[string]bool{
			"presale.read": true, "presale.assign": true,
		},
	}
}

func ts011TeamLead(personID string) Actor {
	return Actor{
		TenantID: "tenant-a", UserID: personID, PersonID: personID,
		Roles:       map[string]bool{"team_lead": true},
		Permissions: map[string]bool{"presale.read": true, "presale.assign": true},
	}
}

func ts011Request(t *testing.T, nodes []byte) (*PresaleRequest, *ApprovalInstance) {
	t.Helper()
	request := &PresaleRequest{
		BaseModel: BaseModel{ID: 9, TenantID: "tenant-a", Version: 3},
		RequestNo: "TS-9", ApplicantID: "sales-a", Status: StatusApprovedPendingAssignment,
	}
	instance := &ApprovalInstance{
		BaseModel: BaseModel{ID: 1, TenantID: "tenant-a", Version: 1},
		RequestID: 9, EngineInstanceID: "crm-presale-9", Status: "APPROVED", NodesJSON: nodes,
	}
	return request, instance
}

func newTS011Service(repo *ts011Repository, directory ownerdirectory.Catalog) *Service {
	return NewService(repo, nil, nil, fixedClock{at: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}, fixedIDs{}).UseOwnerDirectory(directory)
}

func ts011CurrentAssignments(repo *ts011Repository, kind string) []Assignment {
	result := make([]Assignment, 0)
	for _, assignment := range repo.assignments {
		if assignment.IsCurrent && assignment.AssignmentKind == kind {
			result = append(result, assignment)
		}
	}
	return result
}

func findWorkflowNotification(written []WorkflowNotification, recipientID, notificationType string) (WorkflowNotification, bool) {
	for _, notification := range written {
		if notification.RecipientID == recipientID && notification.Type == notificationType {
			return notification, true
		}
	}
	return WorkflowNotification{}, false
}

func TestTwoLevelAssignmentCompletesOnlyThroughSelectedTeamLead(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{
		mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
	}
	service := newTS011Service(repo, ts011NewDirectory())
	notifications := &captureWorkflowNotifications{}
	service.UseWorkflowNotifications(notifications)
	technicalDirector := ts011TechnicalDirector()
	selectedLead := ts011TeamLead("lead-a")
	otherLead := ts011TeamLead("lead-b")
	ctx := context.Background()

	// 技术总监一次提交部门与团队负责人，申请只进入中间态。
	updated, err := service.SelectExecutionDepartment(ctx, technicalDirector, 9, "department-key", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "lead-a", Version: 3,
	})
	if err != nil {
		t.Fatalf("select department and team lead: %v", err)
	}
	if updated.Status != StatusPendingExecutionAssignment || updated.TeamLeadPersonID != "lead-a" ||
		updated.TeamLeadNameSnapshot != "团队负责人甲" || updated.AssignmentLevel != AssignmentLevelTeamLead ||
		updated.ExecutionDepartmentID != "org-1" || updated.ExecutionDepartment != "交付一部" || updated.Version != 4 {
		t.Fatalf("unexpected intermediate request: %+v", updated)
	}
	teamLeads := ts011CurrentAssignments(repo, AssignmentKindTeamLead)
	if len(teamLeads) != 1 || teamLeads[0].AssigneeID != "lead-a" || teamLeads[0].AssigneeRole != "team_lead" {
		t.Fatalf("team lead assignments=%+v", teamLeads)
	}
	if len(ts011CurrentAssignments(repo, AssignmentKindExecutor)) != 0 {
		t.Fatal("level one must not create executor assignments")
	}
	if len(repo.statusLogs) != 1 || repo.statusLogs[0].FromStatus != StatusApprovedPendingAssignment ||
		repo.statusLogs[0].ToStatus != StatusPendingExecutionAssignment || repo.statusLogs[0].Trigger != "TEAM_LEAD_ASSIGNED" {
		t.Fatalf("status logs=%+v", repo.statusLogs)
	}
	if len(repo.events) != 1 || repo.events[0].RecipientPersonID != "lead-a" || repo.events[0].RoleSnapshot != "team_lead" {
		t.Fatalf("team lead evidence=%+v", repo.events)
	}
	if len(repo.outbox) != 0 {
		t.Fatalf("team-lead assignment must not create executor notification outbox yet: %+v", repo.outbox)
	}
	if pending, ok := findWorkflowNotification(notifications.written, "lead-a", "PRESALE_EXECUTOR_ASSIGNMENT_PENDING"); !ok || pending.RequestID != 9 {
		t.Fatalf("team lead pending notification=%+v", notifications.written)
	}
	if _, ok := findWorkflowNotification(notifications.written, "sales-a", "PRESALE_DEPARTMENT_SELECTED"); !ok {
		t.Fatalf("applicant department notification=%+v", notifications.written)
	}

	// 未选中的团队负责人和技术总监都不能越过中间态直接指定执行人。
	if _, err = service.ReplaceExecutorAssignments(ctx, technicalDirector, 9, "td-executor-key", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "越权", Version: 4,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("technical director executor assignment error=%v, want ErrForbidden", err)
	}
	if _, err = service.ReplaceExecutorAssignments(ctx, otherLead, 9, "other-executor-key", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "越权", Version: 4,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other team lead executor assignment error=%v, want ErrForbidden", err)
	}
	if repo.request.Status != StatusPendingExecutionAssignment || len(ts011CurrentAssignments(repo, AssignmentKindExecutor)) != 0 {
		t.Fatal("unauthorized executor assignment changed state")
	}

	// 被指派的团队负责人选择项目经理与本人，申请进入执行中。
	updated, err = service.ReplaceExecutorAssignments(ctx, selectedLead, 9, "executor-key", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{
			{PersonID: "exec-a", Role: "project_manager"},
			{PersonID: "lead-a", Role: "team_lead"},
		},
		ChangeReason: "团队负责人确认执行人", Version: 4,
	})
	if err != nil {
		t.Fatalf("selected team lead executor assignment: %v", err)
	}
	if updated.Status != StatusExecuting || updated.AssignmentLevel != AssignmentLevelExecutor || updated.Version != 5 {
		t.Fatalf("unexpected executing request: %+v", updated)
	}
	executors := ts011CurrentAssignments(repo, AssignmentKindExecutor)
	if len(executors) != 2 || len(ts011CurrentAssignments(repo, AssignmentKindTeamLead)) != 1 {
		t.Fatalf("executors=%+v teamLeads=%+v", executors, ts011CurrentAssignments(repo, AssignmentKindTeamLead))
	}
	if len(repo.statusLogs) != 2 || repo.statusLogs[1].FromStatus != StatusPendingExecutionAssignment ||
		repo.statusLogs[1].ToStatus != StatusExecuting || repo.statusLogs[1].Trigger != "EXECUTORS_ASSIGNED" {
		t.Fatalf("status logs=%+v", repo.statusLogs)
	}
	if len(repo.outbox) != 2 {
		t.Fatalf("executor notification outbox=%d, want 2", len(repo.outbox))
	}
	pendingCount := 0
	for _, notification := range notifications.written {
		if notification.Type == "PRESALE_EXECUTOR_ASSIGNMENT_PENDING" {
			pendingCount++
		}
	}
	if pendingCount != 1 {
		t.Fatalf("executor assignment must not duplicate team-lead workflow notifications: %+v", notifications.written)
	}

	// 精确重放不再新增版本、执行人或通知。
	beforeEvents := len(repo.events)
	beforeOutbox := len(repo.outbox)
	replayed, err := service.ReplaceExecutorAssignments(ctx, selectedLead, 9, "executor-key", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{
			{PersonID: "exec-a", Role: "project_manager"},
			{PersonID: "lead-a", Role: "team_lead"},
		},
		ChangeReason: "团队负责人确认执行人", Version: 4,
	})
	if err != nil || replayed.Version != 5 {
		t.Fatalf("exact replay request=%+v error=%v", replayed, err)
	}
	if len(repo.assignments) != 3 || len(repo.events) != beforeEvents || len(repo.outbox) != beforeOutbox {
		t.Fatalf("replay duplicated writes: assignments=%d events=%d outbox=%d", len(repo.assignments), len(repo.events), len(repo.outbox))
	}
	if _, err = service.ReplaceExecutorAssignments(ctx, selectedLead, 9, "executor-key", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-b", Role: "technician"}}, ChangeReason: "不同载荷", Version: 4,
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different payload replay error=%v, want ErrIdempotencyConflict", err)
	}

	// 执行中仍由当前团队负责人改派，技术总监不能接管；改派不新增第三级负责人。
	updated, err = service.ReplaceExecutorAssignments(ctx, selectedLead, 9, "executor-reassign", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-b", Role: "technician"}}, ChangeReason: "人员调整", Version: 5,
	})
	if err != nil || updated.Status != StatusExecuting || updated.Version != 6 {
		t.Fatalf("executor reassignment request=%+v error=%v", updated, err)
	}
	executors = ts011CurrentAssignments(repo, AssignmentKindExecutor)
	if len(executors) != 1 || executors[0].AssigneeID != "exec-b" {
		t.Fatalf("executors after reassignment=%+v", executors)
	}
	if len(ts011CurrentAssignments(repo, AssignmentKindTeamLead)) != 1 || len(repo.statusLogs) != 2 {
		t.Fatalf("reassignment changed team lead or transition log: teamLeads=%+v logs=%d", ts011CurrentAssignments(repo, AssignmentKindTeamLead), len(repo.statusLogs))
	}
	if _, err = service.ReplaceExecutorAssignments(ctx, technicalDirector, 9, "td-reassign", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "越权", Version: 6,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("technical director reassignment error=%v, want ErrForbidden", err)
	}
}

func TestTwoLevelAssignmentRefusesToSkipTeamLeadAndKeepsLegacyFlow(t *testing.T) {
	t.Run("new flow cannot skip level one", func(t *testing.T) {
		request, instance := ts011Request(t, ts011TwoLevelNodes(t))
		repo := &ts011Repository{
			mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
		}
		service := newTS011Service(repo, ts011NewDirectory())
		technicalDirector := ts011TechnicalDirector()
		ctx := context.Background()

		for name, call := range map[string]func() error{
			"legacy replace assignments": func() error {
				_, err := service.ReplaceAssignments(ctx, technicalDirector, 9, "legacy-key", ReplaceAssignmentsInput{
					Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "skip", Version: 3,
				})
				return err
			},
			"new executor endpoint": func() error {
				_, err := service.ReplaceExecutorAssignments(ctx, technicalDirector, 9, "executor-key", ReplaceAssignmentsInput{
					Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "skip", Version: 3,
				})
				return err
			},
		} {
			if err := call(); !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrForbidden) {
				t.Fatalf("%s error=%v, want invalid transition or forbidden", name, err)
			}
		}
		if repo.request.Status != StatusApprovedPendingAssignment || len(ts011CurrentAssignments(repo, AssignmentKindExecutor)) != 0 {
			t.Fatal("skipping level one changed state")
		}

		// 新流程不接受缺少团队负责人的一级提交。
		if _, err := service.SelectExecutionDepartment(ctx, technicalDirector, 9, "missing-lead", SelectDepartmentInput{DepartmentID: "org-1", Version: 3}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("missing team lead error=%v, want ErrInvalidInput", err)
		}
		if _, err := service.SelectExecutionDepartment(ctx, technicalDirector, 9, "wrong-department", SelectDepartmentInput{
			DepartmentID: "org-1", TeamLeadPersonID: "missing-user", Version: 3,
		}); !errors.Is(err, ownerdirectory.ErrSelectionInvalid) {
			t.Fatalf("invalid team lead error=%v, want selection invalid", err)
		}
		if repo.request.Status != StatusApprovedPendingAssignment || repo.request.TeamLeadPersonID != "" {
			t.Fatal("invalid level one input changed state")
		}
	})

	t.Run("legacy snapshot keeps one-level assignment", func(t *testing.T) {
		request, instance := ts011Request(t, ts011LegacyNodes(t))
		repo := &ts011Repository{
			mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
		}
		service := newTS011Service(repo, ts011NewDirectory())
		technicalDirector := ts011TechnicalDirector()
		ctx := context.Background()

		updated, err := service.SelectExecutionDepartment(ctx, technicalDirector, 9, "legacy-department", SelectDepartmentInput{DepartmentID: "org-1", Version: 3})
		if err != nil || updated.Status != StatusApprovedPendingAssignment || updated.ExecutionDepartmentID != "org-1" {
			t.Fatalf("legacy department request=%+v error=%v", updated, err)
		}
		updated, err = service.ReplaceAssignments(ctx, technicalDirector, 9, "legacy-person", ReplaceAssignmentsInput{
			Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "legacy", Version: updated.Version,
		})
		if err != nil || updated.Status != StatusExecuting {
			t.Fatalf("legacy person assignment request=%+v error=%v", updated, err)
		}
		if len(ts011CurrentAssignments(repo, AssignmentKindTeamLead)) != 0 {
			t.Fatal("legacy flow must not create team lead assignments")
		}
	})
}

func TestTeamLeadRejectionAndInvalidationReturnToPendingAssignment(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{
		mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
	}
	directory := ts011NewDirectory()
	service := newTS011Service(repo, directory)
	notifications := &captureWorkflowNotifications{}
	service.UseWorkflowNotifications(notifications)
	technicalDirector := ts011TechnicalDirector()
	lead := ts011TeamLead("lead-a")
	ctx := context.Background()

	if _, err := service.SelectExecutionDepartment(ctx, technicalDirector, 9, "department-key", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "lead-a", Version: 3,
	}); err != nil {
		t.Fatal(err)
	}

	// 团队负责人拒绝后退回待指派，技术总监可以重新选择负责人。
	updated, err := service.RejectExecutionAssignment(ctx, lead, 9, "reject-key", RejectExecutionAssignmentInput{Reason: "当前无可用资源", Version: 4})
	if err != nil {
		t.Fatalf("reject execution assignment: %v", err)
	}
	if updated.Status != StatusApprovedPendingAssignment || updated.TeamLeadPersonID != "" || updated.AssignmentLevel != AssignmentLevelNone {
		t.Fatalf("request after rejection=%+v", updated)
	}
	if len(ts011CurrentAssignments(repo, AssignmentKindTeamLead)) != 0 {
		t.Fatal("rejected team lead assignment is still current")
	}
	if len(repo.statusLogs) != 2 || repo.statusLogs[1].ToStatus != StatusApprovedPendingAssignment || repo.statusLogs[1].Trigger != "TEAM_LEAD_REJECTED" {
		t.Fatalf("status logs=%+v", repo.statusLogs)
	}
	rejectionNotice, ok := findWorkflowNotification(notifications.written, "technical-director", "PRESALE_EXECUTION_ASSIGNMENT_REJECTED")
	if !ok || rejectionNotice.RequestID != 9 {
		t.Fatalf("technical director rejection notification=%+v", notifications.written)
	}
	// 拒绝成功后团队负责人已被清空，精确重试仍应返回已提交结果而不是 403/409。
	replayed, err := service.RejectExecutionAssignment(ctx, lead, 9, "reject-key", RejectExecutionAssignmentInput{Reason: "当前无可用资源", Version: 4})
	if err != nil || replayed.Status != StatusApprovedPendingAssignment || len(repo.statusLogs) != 2 || repo.request.Version != 5 {
		t.Fatalf("rejection replay request=%+v error=%v logs=%d version=%d", replayed, err, len(repo.statusLogs), repo.request.Version)
	}

	if _, err = service.SelectExecutionDepartment(ctx, technicalDirector, 9, "department-key-2", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "lead-b", Version: 5,
	}); err != nil {
		t.Fatalf("reselect team lead: %v", err)
	}

	// 负责人被停用（目录不再返回该用户）后，不能继续推进，申请回到待重选。
	delete(directory.users, "lead-b")
	if _, err = service.ReplaceExecutorAssignments(ctx, ts011TeamLead("lead-b"), 9, "invalid-lead-executor", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "project_manager"}}, ChangeReason: "invalid lead", Version: 6,
	}); !errors.Is(err, ErrTeamLeadInvalid) {
		t.Fatalf("invalid team lead error=%v, want ErrTeamLeadInvalid", err)
	}
	if repo.request.Status != StatusApprovedPendingAssignment || repo.request.TeamLeadPersonID != "" || repo.request.AssignmentLevel != AssignmentLevelNone {
		t.Fatalf("request did not return to pending assignment: %+v", repo.request)
	}
	if len(ts011CurrentAssignments(repo, AssignmentKindExecutor)) != 0 || len(ts011CurrentAssignments(repo, AssignmentKindTeamLead)) != 0 {
		t.Fatal("invalid team lead left active assignments")
	}
	last := repo.statusLogs[len(repo.statusLogs)-1]
	if last.ToStatus != StatusApprovedPendingAssignment || last.Trigger != "TEAM_LEAD_INVALID" {
		t.Fatalf("invalid team lead status log=%+v", last)
	}
	invalidNotice, ok := findWorkflowNotification(notifications.written, "technical-director", "PRESALE_TEAM_LEAD_INVALID")
	if !ok || invalidNotice.RequestID != 9 {
		t.Fatalf("technical director invalidation notification=%+v", notifications.written)
	}

	// 内部同步/运维入口同样可以把过期的团队负责人退回待重选，且重复调用幂等。
	if _, err = service.SelectExecutionDepartment(ctx, technicalDirector, 9, "department-key-3", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "lead-a", Version: repo.request.Version,
	}); err != nil {
		t.Fatalf("reselect after invalidation: %v", err)
	}
	version := repo.request.Version
	if err = service.InvalidateTeamLead(ctx, "tenant-a", 9, "离职同步"); err != nil {
		t.Fatalf("invalidate team lead: %v", err)
	}
	if repo.request.Status != StatusApprovedPendingAssignment || repo.request.Version != version+1 {
		t.Fatalf("invalidation did not reset request: %+v", repo.request)
	}
	if err = service.InvalidateTeamLead(ctx, "tenant-a", 9, "重复同步"); err != nil {
		t.Fatalf("repeat invalidation: %v", err)
	}
	if repo.request.Version != version+1 {
		t.Fatal("repeat invalidation wrote another state transition")
	}
}

func TestTwoLevelAssignmentDetailExposesOnlyCurrentActorEntry(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{
		mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
	}
	directory := ts011NewDirectory()
	service := newTS011Service(repo, directory)
	technicalDirector := ts011TechnicalDirector()
	selectedLead := ts011TeamLead("lead-a")
	otherLead := ts011TeamLead("lead-b")
	ctx := context.Background()

	detail, err := service.RequestDetail(ctx, technicalDirector, 9)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.AssignmentAction != ApprovalNodeTeamLead || !timelineContains(detail.AvailableActions, "ASSIGN") {
		t.Fatalf("technical director detail action=%q actions=%v", detail.Request.AssignmentAction, detail.AvailableActions)
	}
	if len(detail.Request.AssignmentActions) != 2 || detail.Request.AssignmentActions[0] != ApprovalNodeDepartment || detail.Request.AssignmentActions[1] != ApprovalNodeTeamLead {
		t.Fatalf("technical director actions=%+v", detail.Request.AssignmentActions)
	}
	detail, err = service.RequestDetail(ctx, otherLead, 9)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.AssignmentAction != "" || timelineContains(detail.AvailableActions, "ASSIGN") {
		t.Fatalf("unselected team lead action=%q actions=%v", detail.Request.AssignmentAction, detail.AvailableActions)
	}

	if _, err = service.SelectExecutionDepartment(ctx, technicalDirector, 9, "department-key", SelectDepartmentInput{
		DepartmentID: "org-1", TeamLeadPersonID: "lead-a", Version: 3,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = service.RequestDetail(ctx, selectedLead, 9)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.AssignmentAction != ApprovalNodeExecutor || !timelineContains(detail.AvailableActions, "ASSIGN") {
		t.Fatalf("selected team lead detail action=%q actions=%v", detail.Request.AssignmentAction, detail.AvailableActions)
	}
	detail, err = service.RequestDetail(ctx, technicalDirector, 9)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.AssignmentAction != "" || timelineContains(detail.AvailableActions, "ASSIGN") {
		t.Fatalf("technical director after level one action=%q actions=%v", detail.Request.AssignmentAction, detail.AvailableActions)
	}
	detail, err = service.RequestDetail(ctx, otherLead, 9)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.AssignmentAction != "" || timelineContains(detail.AvailableActions, "ASSIGN") {
		t.Fatalf("unselected team lead after level one action=%q actions=%v", detail.Request.AssignmentAction, detail.AvailableActions)
	}
}

func TestApprovalRuleAcceptsTwoLevelAssignmentWithoutDuplicateActionTypes(t *testing.T) {
	expression := ApprovalExpression{Conditions: []ApprovalCondition{{Field: "urgency", Operator: "eq", Value: "NORMAL"}}}
	valid := ApprovalRule{
		Name: "两级执行指派", Expression: expression,
		Nodes: []ApprovalNode{
			{ID: "sales", Name: "销售审批", Type: ApprovalNodeApproval, RoleCode: "sales_director"},
			{ID: "technical", Name: "技术审批", Type: ApprovalNodeApproval, RoleCode: "technical_director"},
			{ID: "department", Name: "执行部门", Type: ApprovalNodeDepartment, RoleCode: "technical_director"},
			{ID: "team-lead", Name: "团队负责人", Type: ApprovalNodeTeamLead, RoleCode: "technical_director"},
			{ID: "executor", Name: "执行人", Type: ApprovalNodeExecutor, RoleCode: "team_lead"},
		},
	}
	if err := validateApprovalRule(valid); err != nil {
		t.Fatalf("technical director must be allowed to hold department and team-lead actions: %v", err)
	}

	for name, mutate := range map[string]func(*ApprovalRule){
		"duplicate team lead action": func(rule *ApprovalRule) {
			rule.Nodes = append(rule.Nodes, ApprovalNode{ID: "team-lead-2", Name: "重复负责人", Type: ApprovalNodeTeamLead, RoleCode: "team_lead"})
		},
		"team lead without executor": func(rule *ApprovalRule) {
			rule.Nodes = rule.Nodes[:4]
		},
		"executor without team lead": func(rule *ApprovalRule) {
			rule.Nodes = []ApprovalNode{rule.Nodes[0], rule.Nodes[1], rule.Nodes[2], rule.Nodes[4]}
		},
		"mix legacy person and two level": func(rule *ApprovalRule) {
			rule.Nodes = append(rule.Nodes, ApprovalNode{ID: "legacy-person", Name: "旧选人", Type: ApprovalNodePerson, RoleCode: "team_lead"})
		},
		"department and team lead split across roles": func(rule *ApprovalRule) {
			rule.Nodes[3].RoleCode = "team_lead"
		},
	} {
		rule := valid
		rule.Nodes = append([]ApprovalNode(nil), valid.Nodes...)
		mutate(&rule)
		if err := validateApprovalRule(rule); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s error=%v, want ErrInvalidInput", name, err)
		}
	}

	if !UsesTwoLevelAssignment(valid.Nodes) {
		t.Fatal("two-level nodes must be detected")
	}
	if !RoleHasAssignmentAction(valid.Nodes, "technical_director", ApprovalNodeDepartment) ||
		!RoleHasAssignmentAction(valid.Nodes, "technical_director", ApprovalNodeTeamLead) ||
		RoleHasAssignmentAction(valid.Nodes, "technical_director", ApprovalNodeExecutor) {
		t.Fatal("role action lookup did not reflect the configured snapshot")
	}
	if actions := AssignmentActionsForRole(valid.Nodes, "technical_director"); len(actions) != 2 {
		t.Fatalf("technical director actions=%v", actions)
	}
}

func TestCancelAllowsPendingExecutionAssignment(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	request.Status = StatusPendingExecutionAssignment
	request.TeamLeadPersonID = "lead-a"
	request.AssignmentLevel = AssignmentLevelTeamLead
	repo := &ts011Repository{
		mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}},
	}
	service := newTS011Service(repo, ts011NewDirectory())
	lead := ts011TeamLead("lead-a")
	lead.Permissions["presale.cancel"] = true
	if err := service.Cancel(context.Background(), lead, 9, "cancel-key", CancelInput{Reason: "客户取消", Version: 3}); err != nil {
		t.Fatalf("cancel pending execution assignment: %v", err)
	}
	if repo.request.Status != StatusCancelled {
		t.Fatalf("request status=%s", repo.request.Status)
	}
}

func TestWorkflowNotificationExcerptKeepsBodyWithinColumnBound(t *testing.T) {
	excerpt := workflowNotificationExcerpt(strings.Repeat("原", 400), 300)
	if len([]rune(excerpt)) != 301 || !strings.HasSuffix(excerpt, "…") {
		t.Fatalf("excerpt length=%d suffix=%q", len([]rune(excerpt)), excerpt[len(excerpt)-3:])
	}
	if got := workflowNotificationExcerpt("  正常原因  ", 300); got != "正常原因" {
		t.Fatalf("short excerpt=%q", got)
	}
}

func TestExecutingTeamLeadTakeoverKeepsExecutorsAndDoesNotRollbackStatus(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	request.Status, request.Version = StatusExecuting, 6
	request.ExecutionDepartmentID, request.ExecutionDepartment = "org-1", "交付一部"
	request.TeamLeadPersonID, request.TeamLeadNameSnapshot = "lead-a", "团队负责人甲"
	request.AssignmentLevel = AssignmentLevelExecutor
	repo := &ts011Repository{
		mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}, assignments: []Assignment{
			{BaseModel: BaseModel{ID: 1, TenantID: "tenant-a", Version: 1}, RequestID: 9, AssigneeID: "lead-a", AssigneeNameSnapshot: "团队负责人甲", AssigneeDepartmentSnapshot: "交付一部", AssigneeRole: "team_lead", AssignmentKind: AssignmentKindTeamLead, IsCurrent: true, BatchNo: 1},
			{BaseModel: BaseModel{ID: 2, TenantID: "tenant-a", Version: 1}, RequestID: 9, AssigneeID: "exec-a", AssigneeNameSnapshot: "项目经理甲", AssigneeDepartmentSnapshot: "交付一部", AssigneeRole: "project_manager", AssignmentKind: AssignmentKindExecutor, IsCurrent: true, BatchNo: 2},
		}},
	}
	service := newTS011Service(repo, ts011NewDirectory())
	notifications := &captureWorkflowNotifications{}
	service.UseWorkflowNotifications(notifications)
	updated, err := service.TakeoverExecutingTeamLead(context.Background(), ts011TechnicalDirector(), 9, "takeover-executing", TakeoverExecutingTeamLeadInput{
		TeamLeadPersonID: "lead-b", Reason: "原负责人已失效，技术总监接管", Version: 6,
	})
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if updated.Status != StatusExecuting || updated.TeamLeadPersonID != "lead-b" || updated.TeamLeadNameSnapshot != "团队负责人乙" || updated.Version != 7 {
		t.Fatalf("updated request=%+v", updated)
	}
	leads := ts011CurrentAssignments(repo, AssignmentKindTeamLead)
	if len(leads) != 1 || leads[0].AssigneeID != "lead-b" {
		t.Fatalf("current team leads=%+v", leads)
	}
	if len(ts011CurrentAssignments(repo, AssignmentKindExecutor)) != 1 {
		t.Fatal("takeover must preserve current executors")
	}
	if len(repo.statusLogs) != 0 {
		t.Fatalf("takeover must not create a status transition: %+v", repo.statusLogs)
	}
	if _, ok := findWorkflowNotification(notifications.written, "lead-b", "PRESALE_EXECUTOR_ASSIGNMENT_PENDING"); !ok {
		t.Fatalf("takeover notification missing: %+v", notifications.written)
	}
	if _, err = service.TakeoverExecutingTeamLead(context.Background(), ts011TechnicalDirector(), 9, "takeover-executing", TakeoverExecutingTeamLeadInput{TeamLeadPersonID: "lead-b", Reason: "原负责人已失效，技术总监接管", Version: 6}); err != nil {
		t.Fatalf("takeover replay: %v", err)
	}
	if len(ts011CurrentAssignments(repo, AssignmentKindTeamLead)) != 1 {
		t.Fatal("takeover replay created another assignment")
	}
}

type frozenPresaleChecker struct{ frozen string }

func (checker frozenPresaleChecker) ActiveFreezeUser(_ context.Context, _, userID string) (bool, error) {
	return checker.frozen == userID, nil
}

func TestTwoLevelAssignmentRejectsFrozenNewResponsibility(t *testing.T) {
	request, instance := ts011Request(t, ts011TwoLevelNodes(t))
	repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}}
	service := newTS011Service(repo, ts011NewDirectory()).UseFreezeChecker(frozenPresaleChecker{frozen: "lead-b"})
	if _, err := service.SelectExecutionDepartment(context.Background(), ts011TechnicalDirector(), 9, "freeze-level1", SelectDepartmentInput{DepartmentID: "org-1", TeamLeadPersonID: "lead-b", Version: 3}); !errors.Is(err, ErrUserFrozenByHandover) {
		t.Fatalf("frozen team lead err=%v, want ErrUserFrozenByHandover", err)
	}
	request.Status, request.Version = StatusPendingExecutionAssignment, 4
	request.ExecutionDepartmentID, request.ExecutionDepartment = "org-1", "交付一部"
	request.TeamLeadPersonID, request.AssignmentLevel = "lead-a", AssignmentLevelTeamLead
	repo.replays = map[string]*MutationReplay{}
	service = newTS011Service(repo, ts011NewDirectory()).UseFreezeChecker(frozenPresaleChecker{frozen: "exec-a"})
	if _, err := service.ReplaceExecutorAssignments(context.Background(), ts011TeamLead("lead-a"), 9, "freeze-executor", ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "technician"}}, Version: 4}); !errors.Is(err, ErrUserFrozenByHandover) {
		t.Fatalf("frozen executor err=%v, want ErrUserFrozenByHandover", err)
	}
}

func TestLegacyPresaleAssignmentsRejectFrozenNewExecutee(t *testing.T) {
	request, instance := ts011Request(t, ts011LegacyNodes(t))
	repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}}
	service := newTS011Service(repo, ts011NewDirectory()).UseFreezeChecker(frozenPresaleChecker{frozen: "exec-a"})
	if _, err := service.ReplaceAssignments(context.Background(), ts011TechnicalDirector(), 9, "legacy-freeze", ReplaceAssignmentsInput{
		Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "technician"}}, ChangeReason: "历史规则指派", Version: 3,
	}); !errors.Is(err, ErrUserFrozenByHandover) {
		t.Fatalf("legacy frozen executee err=%v, want ErrUserFrozenByHandover", err)
	}
	if repo.request.Status != StatusApprovedPendingAssignment || len(repo.assignments) != 0 {
		t.Fatal("frozen legacy assignment changed state")
	}
}
