package presale

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

func (s *Service) SelectExecutionDepartment(ctx context.Context, actor Actor, id uint64, key string, in SelectDepartmentInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") {
		return nil, ErrForbidden
	}
	in.DepartmentID = strings.TrimSpace(in.DepartmentID)
	in.TeamLeadPersonID = strings.TrimSpace(in.TeamLeadPersonID)
	if in.DepartmentID == "" || in.Version == 0 {
		return nil, ErrInvalidInput
	}
	if in.TeamLeadPersonID == "" {
		return s.selectExecutionDepartmentOnly(ctx, actor, id, in)
	}
	return s.selectExecutionDepartmentAndTeamLead(ctx, actor, id, strings.TrimSpace(key), in)
}

func (s *Service) selectExecutionDepartmentOnly(ctx context.Context, actor Actor, id uint64, in SelectDepartmentInput) (*PresaleRequest, error) {
	if s.ownerDirectory == nil {
		return nil, ErrDependencyUnavailable
	}
	users, err := listOwnerDirectoryUsers(ctx, s.ownerDirectory)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Department)
	found := false
	for _, person := range users {
		for _, org := range person.Organizations {
			if org.ID == in.DepartmentID {
				if name == "" {
					name = org.Name
				}
				found = true
			}
		}
	}
	if !found || name == "" {
		return nil, ownerdirectory.ErrSelectionInvalid
	}
	var out *PresaleRequest
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		instance, instanceErr := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, id)
		if instanceErr != nil && !errors.Is(instanceErr, ErrNotFound) {
			return instanceErr
		}
		if !assignmentActionAllowed(actor, instance, ApprovalNodeDepartment) {
			return ErrForbidden
		}
		// 两级规则必须一次提交团队负责人；只选部门不能推进，也不能把中间态
		// 提前写成已指派。
		if instanceUsesTwoLevelAssignment(instance) {
			return ErrInvalidInput
		}
		if r.Status != StatusApprovedPendingAssignment || r.Version != in.Version {
			return ErrInvalidTransition
		}
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{"execution_department_id": in.DepartmentID, "execution_department": name, "updated_by": actor.UserID}); e != nil {
			return e
		}
		r.ExecutionDepartmentID, r.ExecutionDepartment = in.DepartmentID, name
		if e = s.notifyWorkflow(tx, WorkflowNotification{TenantID: actor.TenantID, RecipientID: r.ApplicantID, Type: "PRESALE_DEPARTMENT_SELECTED", Title: "售前执行部门已确定", Body: "技术总监已选择执行部门：" + name, RequestID: r.ID, RequestNo: r.RequestNo}); e != nil {
			return e
		}
		out = r
		return nil
	})
	return out, err
}

// selectExecutionDepartmentAndTeamLead 完成 TS-011 第①级：权威目录校验
// 团队负责人仍属于所选执行部门，写入负责人投影和 TEAM_LEAD 指派证据，
// 然后把申请推进到 PENDING_EXECUTION_ASSIGNMENT。

// selectExecutionDepartmentAndTeamLead 完成 TS-011 第①级：权威目录校验
// 团队负责人仍属于所选执行部门，写入负责人投影和 TEAM_LEAD 指派证据，
// 然后把申请推进到 PENDING_EXECUTION_ASSIGNMENT。
func (s *Service) selectExecutionDepartmentAndTeamLead(ctx context.Context, actor Actor, id uint64, key string, in SelectDepartmentInput) (*PresaleRequest, error) {
	if key != "" && len(key) > 128 {
		return nil, ErrInvalidInput
	}
	hash, e := mutationDigest(actor, id, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", struct {
		DepartmentID     string `json:"department_id"`
		TeamLeadPersonID string `json:"team_lead_person_id"`
		Version          uint64 `json:"version"`
	}{in.DepartmentID, in.TeamLeadPersonID, in.Version})
	if e != nil {
		return nil, e
	}
	// 精确重放必须先于目录/资格复核返回已提交结果：负责人事后被撤权不能让
	// 同一幂等键的成功请求变成 422，也不能再次调用 owner-directory。
	if key != "" {
		if oldReplay, replayErr := s.repo.FindMutationReplay(ctx, actor.TenantID, id, actor.UserID, key); replayErr == nil {
			if replayErr = validateMutationReplay(oldReplay, actor, id, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash); replayErr != nil {
				return nil, replayErr
			}
			return s.repo.FindRequest(ctx, actor.TenantID, id)
		} else if !errors.Is(replayErr, ErrNotFound) {
			return nil, replayErr
		}
	}
	if s.ownerDirectory == nil {
		return nil, ErrDependencyUnavailable
	}
	resolved, err := s.ownerDirectory.Resolve(ctx, []string{in.TeamLeadPersonID})
	if err != nil {
		return nil, err
	}
	person, ok := resolved[in.TeamLeadPersonID]
	if !ok {
		return nil, ownerdirectory.ErrSelectionInvalid
	}
	departmentName := ""
	for _, organization := range person.Organizations {
		if organization.ID == in.DepartmentID {
			departmentName = strings.TrimSpace(organization.Name)
			break
		}
	}
	if departmentName == "" {
		return nil, ownerdirectory.ErrSelectionInvalid
	}
	if len([]rune(in.DepartmentID)) > 64 || len([]rune(departmentName)) > 128 {
		return nil, ownerdirectory.ErrSelectionInvalid
	}
	teamLeadName := strings.TrimSpace(person.DisplayName)
	if teamLeadName == "" {
		teamLeadName = in.TeamLeadPersonID
	}
	if len([]rune(teamLeadName)) > 128 {
		return nil, ownerdirectory.ErrSelectionInvalid
	}
	if e := s.ensureUserNotFrozen(ctx, actor.TenantID, in.TeamLeadPersonID); e != nil {
		return nil, e
	}
	// 团队负责人必须已具备继续选择执行人的权限；不足时先返回需授权原因，
	// 不写入负责人投影、指派证据或通知。
	if e := s.ensureAssignmentDutyEligible(ctx, AssignmentDutyTeamLead, []AssignmentTarget{{PersonID: in.TeamLeadPersonID}}); e != nil {
		return nil, e
	}
	var out *PresaleRequest
	e = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		instance, instanceErr := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, id)
		if instanceErr != nil && !errors.Is(instanceErr, ErrNotFound) {
			return instanceErr
		}
		if !assignmentActionsAllowed(actor, instance, ApprovalNodeDepartment, ApprovalNodeTeamLead) {
			return ErrForbidden
		}
		if !instanceUsesTwoLevelAssignment(instance) {
			return ErrInvalidTransition
		}
		if key != "" {
			if oldReplay, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
				if findErr = validateMutationReplay(oldReplay, actor, id, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash); findErr != nil {
					return findErr
				}
				out = r
				return nil
			} else if !errors.Is(findErr, ErrNotFound) {
				return findErr
			}
		}
		if r.Status != StatusApprovedPendingAssignment || r.Version != in.Version {
			return ErrInvalidTransition
		}
		now := s.clock.Now()
		if _, e = s.endCurrentTeamLeadAssignment(tx, r, actor, "团队负责人重新指派", now); e != nil {
			return e
		}
		assignments, e := s.repo.ListAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		batch := uint64(1)
		for _, assignment := range assignments {
			if assignment.BatchNo >= batch {
				batch = assignment.BatchNo + 1
			}
		}
		teamLeadAssignment := &Assignment{
			BaseModel:                  BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1},
			RequestID:                  id,
			AssigneeID:                 in.TeamLeadPersonID,
			AssigneeNamespace:          ProgressRecipientUser,
			AssigneeNameSnapshot:       teamLeadName,
			AssigneeDepartmentSnapshot: departmentName,
			AssigneeRole:               "team_lead",
			AssignmentKind:             AssignmentKindTeamLead,
			AssignedBy:                 actor.UserID,
			AssignedAt:                 now,
			IsCurrent:                  true,
			BatchNo:                    batch,
			ChangeReason:               "团队负责人指派",
		}
		if e = s.repo.CreateAssignment(tx, teamLeadAssignment); e != nil {
			return e
		}
		if _, e = s.recordAssignmentEvent(tx, actor, r, teamLeadAssignment, AssignmentEventAdded, teamLeadAssignment.ChangeReason, now); e != nil {
			return e
		}
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{
			"status": StatusPendingExecutionAssignment, "execution_department_id": in.DepartmentID,
			"execution_department": departmentName, "team_lead_person_id": in.TeamLeadPersonID,
			"team_lead_name_snapshot": teamLeadName, "assignment_level": AssignmentLevelTeamLead,
			"updated_by": actor.UserID,
		}); e != nil {
			return e
		}
		r.Status, r.ExecutionDepartmentID, r.ExecutionDepartment = StatusPendingExecutionAssignment, in.DepartmentID, departmentName
		r.TeamLeadPersonID, r.TeamLeadNameSnapshot, r.AssignmentLevel = in.TeamLeadPersonID, teamLeadName, AssignmentLevelTeamLead
		if e = s.statusLog(tx, r, StatusApprovedPendingAssignment, StatusPendingExecutionAssignment, "TEAM_LEAD_ASSIGNED", "", actor.UserID, actor.RequestID); e != nil {
			return e
		}
		if e = s.notifyWorkflow(tx, WorkflowNotification{
			TenantID: actor.TenantID, RecipientID: in.TeamLeadPersonID, Type: "PRESALE_EXECUTOR_ASSIGNMENT_PENDING",
			Title:     "售前执行指派待处理",
			Body:      "您已被指定为售前申请 " + r.RequestNo + " 的团队负责人，请选择执行人。",
			RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: fmt.Sprintf("team-lead:%d", teamLeadAssignment.ID),
		}); e != nil {
			return e
		}
		if e = s.notifyWorkflow(tx, WorkflowNotification{
			TenantID: actor.TenantID, RecipientID: r.ApplicantID, Type: "PRESALE_DEPARTMENT_SELECTED",
			Title:     "售前执行部门和团队负责人已确定",
			Body:      "技术总监已选择执行部门：" + departmentName + "，团队负责人：" + teamLeadName,
			RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: fmt.Sprintf("team-lead:%d", teamLeadAssignment.ID),
		}); e != nil {
			return e
		}
		if key != "" {
			if e = s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash, r.Version, now)); e != nil {
				return e
			}
		}
		out = r
		return nil
	})
	if e != nil && key != "" && isDuplicateMutationError(e) {
		var replayed *PresaleRequest
		e = s.resolveMutationRace(ctx, actor, id, key, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash, e, nil, func(value *PresaleRequest) { replayed = value })
		if e == nil {
			out = replayed
		}
	}
	return out, e
}

// TakeoverExecutingTeamLead 允许技术总监或 CRM 超级管理员
// 替换 EXECUTING 请求的组长，无需回退请求、
// 终止执行人员或丢弃工时和进度。关闭已有组长
// 指派，并追加新的不可变指派。

// TakeoverExecutingTeamLead 允许技术总监或 CRM 超级管理员
// 替换 EXECUTING 请求的组长，无需回退请求、
// 终止执行人员或丢弃工时和进度。关闭已有组长
// 指派，并追加新的不可变指派。
