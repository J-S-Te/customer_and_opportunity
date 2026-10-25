package presale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"sort"
	"strings"
	"time"
)

// 分派采用“目标集合替换”语义：保留集合不重复写入，移除项结束历史关系，执行人来自
// 基础平台授权人员选择器。首次有效分派会把申请推进到 EXECUTING；执行中
// 调整人员后还会重新判断所有当前执行人是否已有有效工时，从而可能立即完成任务。
func (s *Service) ReplaceAssignments(ctx context.Context, actor Actor, id uint64, key string, in ReplaceAssignmentsInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	in.ChangeReason = strings.TrimSpace(in.ChangeReason)
	if key == "" || len(key) > 128 || len(in.Assignees) == 0 || len(in.Assignees) > 100 || len([]rune(in.ChangeReason)) > 1000 {
		return nil, ErrInvalidInput
	}
	targets, _, canonicalTargets, e := normalizeAssignmentTargets(in.Assignees)
	if e != nil {
		return nil, e
	}
	hash, e := mutationDigest(actor, id, "REPLACE_ASSIGNMENTS", "REPLACE", struct {
		Assignees    []AssignmentTarget `json:"assignees"`
		ChangeReason string             `json:"change_reason"`
		Version      uint64             `json:"version"`
	}{assignmentIntentTargets(canonicalTargets), in.ChangeReason, in.Version})
	if e != nil {
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
		if !assignmentActionAllowed(actor, instance, ApprovalNodePerson) {
			return ErrForbidden
		}
		// TS-011 新规则必须经过第①级团队负责人。旧接口只为历史一级指派规则保留，
		// 不能让具备旧 PERSON_ASSIGNMENT 动作的调用者绕过中间态直接推进。
		if instanceUsesTwoLevelAssignment(instance) || r.Status == StatusPendingExecutionAssignment || strings.TrimSpace(r.TeamLeadPersonID) != "" {
			return ErrInvalidTransition
		}
		if oldReplay, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			if findErr = validateMutationReplay(oldReplay, actor, id, "REPLACE_ASSIGNMENTS", "REPLACE", hash); findErr != nil {
				return findErr
			}
			out = r
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Version != in.Version {
			return ErrVersionConflict
		}
		if r.Status != StatusApprovedPendingAssignment && r.Status != StatusExecuting {
			return ErrInvalidTransition
		}
		old, e := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		current := map[string]Assignment{}
		batch := uint64(1)
		for _, v := range old {
			current[v.AssigneeID] = v
			if v.BatchNo >= batch {
				batch = v.BatchNo + 1
			}
		}
		// 已提交精确重放在上方返回；新写入才读取可变目录与当前资格。
		targets, _, canonicalTargets, e = s.resolveAssignmentTargets(tx, in.Assignees)
		if e != nil {
			return e
		}
		newAssignees := make([]AssignmentTarget, 0, len(canonicalTargets))
		for _, target := range canonicalTargets {
			if _, exists := current[target.PersonID]; exists {
				continue
			}
			if e = s.ensureUserNotFrozen(tx, actor.TenantID, target.PersonID); e != nil {
				return e
			}
			newAssignees = append(newAssignees, target)
		}
		if e = s.ensureAssignmentDutyEligible(tx, AssignmentDutyExecutor, newAssignees); e != nil {
			return e
		}
		now := s.clock.Now()
		for pid, a := range current {
			if _, ok := targets[pid]; !ok {
				if e = s.repo.EndAssignment(tx, actor.TenantID, a.ID, a.Version, actor.UserID, now); e != nil {
					return e
				}
				if e = s.recordAssignmentNotification(tx, actor, r, &a, AssignmentEventRemoved, in.ChangeReason, now); e != nil {
					return e
				}
			}
		}
		for pid, t := range targets {
			if _, ok := current[pid]; ok {
				continue
			}
			personName := t.PersonName
			if personName == "" {
				personName = pid
			}
			a := &Assignment{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestID: id, AssigneeID: pid, AssigneeNamespace: ProgressRecipientUser, AssigneeNameSnapshot: personName, AssigneeDepartmentSnapshot: t.Department, AssigneeRole: t.Role, AssignedBy: actor.UserID, AssignedAt: now, IsCurrent: true, BatchNo: batch, ChangeReason: in.ChangeReason}
			if e = s.repo.CreateAssignment(tx, a); e != nil {
				return e
			}
			if e = s.recordAssignmentNotification(tx, actor, r, a, AssignmentEventAdded, in.ChangeReason, now); e != nil {
				return e
			}
		}
		from := r.Status
		if from == StatusApprovedPendingAssignment {
			if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{"status": StatusExecuting, "updated_by": actor.UserID}); e != nil {
				return e
			}
			if e = s.statusLog(tx, r, from, StatusExecuting, "ASSIGNED", in.ChangeReason, actor.UserID, actor.RequestID); e != nil {
				return e
			}
			r.Status = StatusExecuting
		} else {
			if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{"updated_by": actor.UserID}); e != nil {
				return e
			}
		}
		fresh, e := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		completed, e := s.completeIfReady(tx, r, fresh, actor.UserID, actor.RequestID)
		if e != nil {
			return e
		}
		if completed {
			r.Status = StatusCompleted
		}
		if e = s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "REPLACE_ASSIGNMENTS", "REPLACE", hash, r.Version, now)); e != nil {
			return e
		}
		out = r
		return nil
	})
	if e != nil && isDuplicateMutationError(e) {
		var replayed *PresaleRequest
		e = s.resolveMutationRace(ctx, actor, id, key, "REPLACE_ASSIGNMENTS", "REPLACE", hash, e, nil, func(value *PresaleRequest) { replayed = value })
		if e == nil {
			out = replayed
		}
	}
	return out, e
}

// SelectExecutionDepartment 同时承载 TS-003 的一级部门选择和 TS-011 的
// “部门 + 团队负责人”第①级。请求体没有团队负责人时保持旧接口行为；
// 新规则快照下的旧调用会在事务内被拒绝，避免绕过中间态。

// SelectExecutionDepartment 同时承载 TS-003 的一级部门选择和 TS-011 的
// “部门 + 团队负责人”第①级。请求体没有团队负责人时保持旧接口行为；
// 新规则快照下的旧调用会在事务内被拒绝，避免绕过中间态。
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
func (s *Service) TakeoverExecutingTeamLead(ctx context.Context, actor Actor, id uint64, key string, in TakeoverExecutingTeamLeadInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") || (!actor.HasRole("technical_director") && !actor.HasRole("crm_super_admin")) {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	in.TeamLeadPersonID = strings.TrimSpace(in.TeamLeadPersonID)
	in.Reason = strings.TrimSpace(in.Reason)
	if key == "" || len(key) > 128 || in.TeamLeadPersonID == "" || in.Version == 0 || len([]rune(in.Reason)) > 1000 {
		return nil, ErrInvalidInput
	}
	hash, err := mutationDigest(actor, id, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", struct {
		TeamLeadPersonID string `json:"team_lead_person_id"`
		Version          uint64 `json:"version"`
		Reason           string `json:"reason"`
	}{in.TeamLeadPersonID, in.Version, in.Reason})
	if err != nil {
		return nil, err
	}
	// 精确重放必须先于接管后的版本和组长检查。
	if oldReplay, replayErr := s.repo.FindMutationReplay(ctx, actor.TenantID, id, actor.UserID, key); replayErr == nil {
		if replayErr = validateMutationReplay(oldReplay, actor, id, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash); replayErr != nil {
			return nil, replayErr
		}
		return s.repo.FindRequest(ctx, actor.TenantID, id)
	} else if !errors.Is(replayErr, ErrNotFound) {
		return nil, replayErr
	}
	pre, err := s.repo.FindRequest(ctx, actor.TenantID, id)
	if err != nil {
		return nil, err
	}
	if pre.Status != StatusExecuting || pre.Version != in.Version || pre.TeamLeadPersonID == "" {
		return nil, ErrInvalidTransition
	}
	if in.TeamLeadPersonID == pre.TeamLeadPersonID {
		return nil, ErrInvalidTransition
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
	organizationID := strings.TrimSpace(pre.ExecutionDepartmentID)
	departmentName := ""
	for _, organization := range person.Organizations {
		if organization.ID == organizationID {
			departmentName = strings.TrimSpace(organization.Name)
			break
		}
	}
	if departmentName == "" {
		return nil, ownerdirectory.ErrSelectionInvalid
	}
	teamLeadName := strings.TrimSpace(person.DisplayName)
	if teamLeadName == "" {
		teamLeadName = in.TeamLeadPersonID
	}
	if err = s.ensureUserNotFrozen(ctx, actor.TenantID, in.TeamLeadPersonID); err != nil {
		return nil, err
	}
	// 执行中接管同样先核验新负责人是否具备继续管理执行人的权限。
	if err = s.ensureAssignmentDutyEligible(ctx, AssignmentDutyTeamLead, []AssignmentTarget{{PersonID: in.TeamLeadPersonID}}); err != nil {
		return nil, err
	}
	var out *PresaleRequest
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, txErr := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if txErr != nil {
			return txErr
		}
		if oldReplay, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			if findErr = validateMutationReplay(oldReplay, actor, id, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash); findErr != nil {
				return findErr
			}
			out = r
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Status != StatusExecuting || r.Version != in.Version || r.TeamLeadPersonID == "" || r.TeamLeadPersonID == in.TeamLeadPersonID {
			return ErrInvalidTransition
		}
		now := s.clock.Now()
		if _, txErr = s.endCurrentTeamLeadAssignment(tx, r, actor, "执行中负责人接管", now); txErr != nil {
			return txErr
		}
		assignments, txErr := s.repo.ListAssignmentsForUpdate(tx, actor.TenantID, id)
		if txErr != nil {
			return txErr
		}
		batch := uint64(1)
		for _, assignment := range assignments {
			if assignment.BatchNo >= batch {
				batch = assignment.BatchNo + 1
			}
		}
		teamLeadAssignment := &Assignment{
			BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1},
			RequestID: id, AssigneeID: in.TeamLeadPersonID, AssigneeNamespace: ProgressRecipientUser, AssigneeNameSnapshot: teamLeadName,
			AssigneeDepartmentSnapshot: departmentName, AssigneeRole: "team_lead",
			AssignmentKind: AssignmentKindTeamLead, AssignedBy: actor.UserID, AssignedAt: now,
			IsCurrent: true, BatchNo: batch, ChangeReason: "执行中负责人接管",
		}
		if txErr = s.repo.CreateAssignment(tx, teamLeadAssignment); txErr != nil {
			return txErr
		}
		if _, txErr = s.recordAssignmentEvent(tx, actor, r, teamLeadAssignment, AssignmentEventAdded, teamLeadAssignment.ChangeReason, now); txErr != nil {
			return txErr
		}
		if txErr = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{
			"team_lead_person_id": in.TeamLeadPersonID, "team_lead_name_snapshot": teamLeadName,
			"updated_by": actor.UserID,
		}); txErr != nil {
			return txErr
		}
		r.TeamLeadPersonID, r.TeamLeadNameSnapshot = in.TeamLeadPersonID, teamLeadName
		if txErr = s.notifyWorkflow(tx, WorkflowNotification{
			TenantID: actor.TenantID, RecipientID: in.TeamLeadPersonID, Type: "PRESALE_EXECUTOR_ASSIGNMENT_PENDING",
			Title:     "执行中申请负责人已接管",
			Body:      "您已接管售前申请 " + r.RequestNo + " 的团队负责人职责，请继续管理当前执行人。",
			RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: fmt.Sprintf("takeover:%d", teamLeadAssignment.ID),
		}); txErr != nil {
			return txErr
		}
		if txErr = s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash, r.Version, now)); txErr != nil {
			return txErr
		}
		out = r
		return nil
	})
	if err != nil && key != "" && isDuplicateMutationError(err) {
		var replayed *PresaleRequest
		err = s.resolveMutationRace(ctx, actor, id, key, "SELECT_EXECUTION_DEPARTMENT", "TEAM_LEAD", hash, err, nil, func(value *PresaleRequest) { replayed = value })
		if err == nil {
			out = replayed
		}
	}
	return out, err
}

// ReplaceExecutorAssignments 完成 TS-011 第②级。授权主体不是规则角色，
// 而是申请上当前记录的团队负责人本人；精确重放仍返回当前任务投影。

// ReplaceExecutorAssignments 完成 TS-011 第②级。授权主体不是规则角色，
// 而是申请上当前记录的团队负责人本人；精确重放仍返回当前任务投影。
func (s *Service) ReplaceExecutorAssignments(ctx context.Context, actor Actor, id uint64, key string, in ReplaceAssignmentsInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	in.ChangeReason = strings.TrimSpace(in.ChangeReason)
	if key == "" || len(key) > 128 || len(in.Assignees) == 0 || len(in.Assignees) > 100 || len([]rune(in.ChangeReason)) > 1000 {
		return nil, ErrInvalidInput
	}
	targets, ids, canonicalTargets, e := normalizeAssignmentTargets(in.Assignees)
	if e != nil {
		return nil, e
	}
	hash, e := mutationDigest(actor, id, "REPLACE_EXECUTOR_ASSIGNMENTS", "REPLACE", struct {
		Assignees    []AssignmentTarget `json:"assignees"`
		ChangeReason string             `json:"change_reason"`
		Version      uint64             `json:"version"`
	}{assignmentIntentTargets(canonicalTargets), in.ChangeReason, in.Version})
	if e != nil {
		return nil, e
	}
	pre, e := s.repo.FindRequest(ctx, actor.TenantID, id)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(pre.TeamLeadPersonID) == "" {
		return nil, ErrInvalidTransition
	}
	if !actor.HasRole("crm_super_admin") && actor.PersonID != pre.TeamLeadPersonID {
		return nil, ErrForbidden
	}
	// 请求意图只绑定稳定输入；当前调用者权限、租户和负责人仍须先通过。
	if oldReplay, findErr := s.repo.FindMutationReplay(ctx, actor.TenantID, id, actor.UserID, key); findErr == nil {
		if findErr = validateMutationReplay(oldReplay, actor, id, "REPLACE_EXECUTOR_ASSIGNMENTS", "REPLACE", hash); findErr != nil {
			return nil, findErr
		}
		return pre, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	targets, ids, canonicalTargets, e = s.resolveAssignmentTargets(ctx, in.Assignees)
	if e != nil {
		return nil, e
	}
	existing, e := s.repo.ListAssignments(ctx, actor.TenantID, id)
	if e != nil {
		return nil, e
	}
	existingExecutors := map[string]struct{}{}
	for _, assignment := range existing {
		if assignment.IsCurrent && assignment.AssignmentKind == AssignmentKindExecutor {
			existingExecutors[assignment.AssigneeID] = struct{}{}
		}
	}
	newAssignees := make([]AssignmentTarget, 0, len(targets))
	for _, target := range targets {
		if _, ok := existingExecutors[target.PersonID]; ok {
			continue
		}
		if e = s.ensureUserNotFrozen(ctx, actor.TenantID, target.PersonID); e != nil {
			return nil, e
		}
		newAssignees = append(newAssignees, target)
	}
	// 只核验新增执行人；既有指派在精确重放时仍按已提交结果返回。
	if e = s.ensureAssignmentDutyEligible(ctx, AssignmentDutyExecutor, newAssignees); e != nil {
		return nil, e
	}
	if !actor.HasRole("crm_super_admin") {
		if e = s.revalidateTeamLead(ctx, pre); e != nil {
			return nil, e
		}
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
		if !instanceUsesTwoLevelAssignment(instance) {
			return ErrInvalidTransition
		}
		if strings.TrimSpace(r.TeamLeadPersonID) == "" {
			return ErrInvalidTransition
		}
		if !actor.HasRole("crm_super_admin") && actor.PersonID != r.TeamLeadPersonID {
			return ErrForbidden
		}
		if oldReplay, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			if findErr = validateMutationReplay(oldReplay, actor, id, "REPLACE_EXECUTOR_ASSIGNMENTS", "REPLACE", hash); findErr != nil {
				return findErr
			}
			out = r
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Version != in.Version {
			return ErrVersionConflict
		}
		if r.Status != StatusPendingExecutionAssignment && r.Status != StatusExecuting {
			return ErrInvalidTransition
		}
		assignments, e := s.repo.ListAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		current := map[string]Assignment{}
		batch := uint64(1)
		for _, assignment := range assignments {
			if assignment.BatchNo >= batch {
				batch = assignment.BatchNo + 1
			}
			if assignment.IsCurrent && assignment.EffectiveAssignmentKind() == AssignmentKindExecutor {
				current[assignment.AssigneeID] = assignment
			}
		}
		now := s.clock.Now()
		for personID, assignment := range current {
			if _, keep := targets[personID]; keep {
				continue
			}
			if e = s.repo.EndAssignment(tx, actor.TenantID, assignment.ID, assignment.Version, actor.UserID, now); e != nil {
				return e
			}
			if e = s.recordAssignmentNotification(tx, actor, r, &assignment, AssignmentEventRemoved, in.ChangeReason, now); e != nil {
				return e
			}
		}
		for _, personID := range ids {
			if _, exists := current[personID]; exists {
				continue
			}
			target := targets[personID]
			name := target.PersonName
			if name == "" {
				name = personID
			}
			assignment := &Assignment{
				BaseModel:                  BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1},
				RequestID:                  id,
				AssigneeID:                 personID,
				AssigneeNamespace:          ProgressRecipientUser,
				AssigneeNameSnapshot:       name,
				AssigneeDepartmentSnapshot: target.Department,
				AssigneeRole:               target.Role,
				AssignmentKind:             AssignmentKindExecutor,
				AssignedBy:                 actor.UserID,
				AssignedAt:                 now,
				IsCurrent:                  true,
				BatchNo:                    batch,
				ChangeReason:               in.ChangeReason,
			}
			if e = s.repo.CreateAssignment(tx, assignment); e != nil {
				return e
			}
			if e = s.recordAssignmentNotification(tx, actor, r, assignment, AssignmentEventAdded, in.ChangeReason, now); e != nil {
				return e
			}
		}
		from := r.Status
		fields := map[string]any{"assignment_level": AssignmentLevelExecutor, "updated_by": actor.UserID}
		if from == StatusPendingExecutionAssignment {
			fields["status"] = StatusExecuting
		}
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, fields); e != nil {
			return e
		}
		r.AssignmentLevel = AssignmentLevelExecutor
		if from == StatusPendingExecutionAssignment {
			r.Status = StatusExecuting
			if e = s.statusLog(tx, r, from, StatusExecuting, "EXECUTORS_ASSIGNED", in.ChangeReason, actor.UserID, actor.RequestID); e != nil {
				return e
			}
		}
		fresh, e := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		completed, e := s.completeIfReady(tx, r, fresh, actor.UserID, actor.RequestID)
		if e != nil {
			return e
		}
		if completed {
			r.Status = StatusCompleted
		}
		if e = s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "REPLACE_EXECUTOR_ASSIGNMENTS", "REPLACE", hash, r.Version, now)); e != nil {
			return e
		}
		out = r
		return nil
	})
	if e != nil && isDuplicateMutationError(e) {
		var replayed *PresaleRequest
		e = s.resolveMutationRace(ctx, actor, id, key, "REPLACE_EXECUTOR_ASSIGNMENTS", "REPLACE", hash, e, func(value *PresaleRequest) error {
			if !actor.HasRole("crm_super_admin") && value.TeamLeadPersonID != actor.PersonID {
				return ErrForbidden
			}
			if value.Status != StatusPendingExecutionAssignment && value.Status != StatusExecuting {
				return ErrInvalidTransition
			}
			return nil
		}, func(value *PresaleRequest) { replayed = value })
		if e == nil {
			out = replayed
		}
	}
	return out, e
}

// RejectExecutionAssignment 对应 Q4：团队负责人拒绝后退回待指派，由技术总监重选。

// RejectExecutionAssignment 对应 Q4：团队负责人拒绝后退回待指派，由技术总监重选。
func (s *Service) RejectExecutionAssignment(ctx context.Context, actor Actor, id uint64, key string, in RejectExecutionAssignmentInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	in.Reason = strings.TrimSpace(in.Reason)
	if key == "" || len(key) > 128 || in.Reason == "" || len([]rune(in.Reason)) > 1000 || in.Version == 0 {
		return nil, ErrInvalidInput
	}
	hash, e := mutationDigest(actor, id, "REJECT_EXECUTION_ASSIGNMENT", "REJECT", struct {
		Reason  string `json:"reason"`
		Version uint64 `json:"version"`
	}{in.Reason, in.Version})
	if e != nil {
		return nil, e
	}
	// 拒绝成功后团队负责人会被清空，精确重试必须仍能返回当时的已提交结果。
	if pre, findErr := s.repo.FindRequest(ctx, actor.TenantID, id); findErr == nil {
		if oldReplay, replayErr := s.repo.FindMutationReplay(ctx, actor.TenantID, id, actor.UserID, key); replayErr == nil {
			if replayErr = validateMutationReplay(oldReplay, actor, id, "REJECT_EXECUTION_ASSIGNMENT", "REJECT", hash); replayErr != nil {
				return nil, replayErr
			}
			return pre, nil
		} else if !errors.Is(replayErr, ErrNotFound) {
			return nil, replayErr
		}
	} else {
		return nil, findErr
	}
	var out *PresaleRequest
	e = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		// 重放记录绑定操作者、父申请与规范载荷，因此可由同一操作者安全重试；
		// 新请求再校验当前团队负责人和状态。
		if oldReplay, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			if findErr = validateMutationReplay(oldReplay, actor, id, "REJECT_EXECUTION_ASSIGNMENT", "REJECT", hash); findErr != nil {
				return findErr
			}
			out = r
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.TeamLeadPersonID == "" || (!actor.HasRole("crm_super_admin") && actor.PersonID != r.TeamLeadPersonID) {
			return ErrForbidden
		}
		if r.Status != StatusPendingExecutionAssignment {
			return ErrInvalidTransition
		}
		if r.Version != in.Version {
			return ErrVersionConflict
		}
		now := s.clock.Now()
		if _, e = s.endCurrentTeamLeadAssignment(tx, r, actor, in.Reason, now); e != nil {
			return e
		}
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{
			"status": StatusApprovedPendingAssignment, "team_lead_person_id": "",
			"team_lead_name_snapshot": "", "assignment_level": AssignmentLevelNone,
			"updated_by": actor.UserID,
		}); e != nil {
			return e
		}
		r.Status, r.TeamLeadPersonID, r.TeamLeadNameSnapshot, r.AssignmentLevel = StatusApprovedPendingAssignment, "", "", AssignmentLevelNone
		if e = s.statusLog(tx, r, StatusPendingExecutionAssignment, StatusApprovedPendingAssignment, "TEAM_LEAD_REJECTED", in.Reason, actor.UserID, actor.RequestID); e != nil {
			return e
		}
		if e = s.notifyRoleUsers(tx, actor.TenantID, "technical_director", WorkflowNotification{
			Type: "PRESALE_EXECUTION_ASSIGNMENT_REJECTED", Title: "团队负责人拒绝执行指派",
			Body:      "售前申请 " + r.RequestNo + " 的团队负责人拒绝执行指派：" + workflowNotificationExcerpt(in.Reason, 300) + "。请重新选择团队负责人。",
			RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: fmt.Sprintf("reject:%d", r.Version),
		}); e != nil {
			return e
		}
		if e = s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "REJECT_EXECUTION_ASSIGNMENT", "REJECT", hash, r.Version, now)); e != nil {
			return e
		}
		out = r
		return nil
	})
	if e != nil && isDuplicateMutationError(e) {
		var replayed *PresaleRequest
		e = s.resolveMutationRace(ctx, actor, id, key, "REJECT_EXECUTION_ASSIGNMENT", "REJECT", hash, e, nil, func(value *PresaleRequest) { replayed = value })
		if e == nil {
			out = replayed
		}
	}
	return out, e
}

// InvalidateTeamLead 供人员同步或受控运维处理团队负责人失效。
// 待执行指派申请退回 APPROVED_PENDING_ASSIGNMENT；EXECUTING 申请保持执行中，
// 只通知技术总监人工接管，避免自动中断已发生的工作和工时事实。

// InvalidateTeamLead 供人员同步或受控运维处理团队负责人失效。
// 待执行指派申请退回 APPROVED_PENDING_ASSIGNMENT；EXECUTING 申请保持执行中，
// 只通知技术总监人工接管，避免自动中断已发生的工作和工时事实。
func (s *Service) InvalidateTeamLead(ctx context.Context, tenant string, requestID uint64, reason string) error {
	reason = strings.TrimSpace(reason)
	if tenant == "" || requestID == 0 {
		return ErrInvalidInput
	}
	if len([]rune(reason)) > 1000 {
		return ErrInvalidInput
	}
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, tenant, requestID)
		if e != nil {
			return e
		}
		if r.TeamLeadPersonID == "" {
			return nil
		}
		if r.Status == StatusExecuting {
			return s.notifyRoleUsers(tx, tenant, "technical_director", WorkflowNotification{
				Type: "PRESALE_TEAM_LEAD_INVALID", Title: "执行中申请的团队负责人已失效",
				Body:      "售前申请 " + r.RequestNo + " 的团队负责人已失效，申请仍保持执行中；请人工确认接管或重新指派。",
				RequestID: r.ID, RequestNo: r.RequestNo,
				SourceDiscriminator: "executing-invalid:" + r.TeamLeadPersonID,
			})
		}
		if r.Status != StatusPendingExecutionAssignment {
			return nil
		}
		now := s.clock.Now()
		system := Actor{TenantID: tenant, UserID: "presale-team-lead-invalidation"}
		if _, e = s.endCurrentTeamLeadAssignment(tx, r, system, reason, now); e != nil {
			return e
		}
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{
			"status": StatusApprovedPendingAssignment, "team_lead_person_id": "",
			"team_lead_name_snapshot": "", "assignment_level": AssignmentLevelNone,
			"updated_by": system.UserID,
		}); e != nil {
			return e
		}
		r.Status, r.TeamLeadPersonID, r.TeamLeadNameSnapshot, r.AssignmentLevel = StatusApprovedPendingAssignment, "", "", AssignmentLevelNone
		if e = s.statusLog(tx, r, StatusPendingExecutionAssignment, StatusApprovedPendingAssignment, "TEAM_LEAD_INVALID", reason, system.UserID, ""); e != nil {
			return e
		}
		return s.notifyRoleUsers(tx, tenant, "technical_director", WorkflowNotification{
			Type: "PRESALE_TEAM_LEAD_INVALID", Title: "团队负责人已失效",
			Body:      "售前申请 " + r.RequestNo + " 的团队负责人已失效，申请已退回待指派，请重新选择负责人。",
			RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: fmt.Sprintf("invalid:%d", r.Version),
		})
	})
}

func (s *Service) revalidateTeamLead(ctx context.Context, request *PresaleRequest) error {
	if request == nil || strings.TrimSpace(request.TeamLeadPersonID) == "" {
		return ErrInvalidTransition
	}
	if s.ownerDirectory == nil {
		return ErrDependencyUnavailable
	}
	resolved, err := s.ownerDirectory.Resolve(ctx, []string{request.TeamLeadPersonID})
	if err != nil {
		return err
	}
	person, ok := resolved[request.TeamLeadPersonID]
	activeInDepartment := false
	if ok {
		for _, organization := range person.Organizations {
			if organization.ID == request.ExecutionDepartmentID {
				activeInDepartment = true
				break
			}
		}
	}
	if activeInDepartment {
		return nil
	}
	if request.Status == StatusPendingExecutionAssignment {
		if err = s.InvalidateTeamLead(ctx, request.TenantID, request.ID, "团队负责人已离职、调岗或停用"); err != nil {
			return err
		}
	}
	return ErrTeamLeadInvalid
}

// endCurrentTeamLeadAssignment 结束当前 TEAM_LEAD 指派并追加不可变移出证据。
// 返回是否确实结束了一条当前指派，便于调用方区分重复失效与真实回退。

// endCurrentTeamLeadAssignment 结束当前 TEAM_LEAD 指派并追加不可变移出证据。
// 返回是否确实结束了一条当前指派，便于调用方区分重复失效与真实回退。
func (s *Service) endCurrentTeamLeadAssignment(ctx context.Context, r *PresaleRequest, actor Actor, reason string, at time.Time) (bool, error) {
	assignments, err := s.repo.ListAssignmentsForUpdate(ctx, actor.TenantID, r.ID)
	if err != nil {
		return false, err
	}
	ended := false
	for _, assignment := range assignments {
		if !assignment.IsCurrent || assignment.EffectiveAssignmentKind() != AssignmentKindTeamLead {
			continue
		}
		if err = s.repo.EndAssignment(ctx, actor.TenantID, assignment.ID, assignment.Version, actor.UserID, at); err != nil {
			return false, err
		}
		assignment.IsCurrent = false
		assignment.EndedAt = &at
		assignment.UpdatedBy = actor.UserID
		assignment.Version++
		if _, err = s.recordAssignmentEvent(ctx, actor, r, &assignment, AssignmentEventRemoved, reason, at); err != nil {
			return false, err
		}
		ended = true
	}
	return ended, nil
}

// resolveAssignmentTargets 把浏览器选择的执行人转换为权威目录快照。
// 组织归属和姓名始终以基础平台目录为准，不信任请求体里的展示字段。

// resolveAssignmentTargets 把浏览器选择的执行人转换为权威目录快照。
// 组织归属和姓名始终以基础平台目录为准，不信任请求体里的展示字段。
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

func (s *Service) resolveAssignmentTargets(ctx context.Context, assignees []AssignmentTarget) (map[string]AssignmentTarget, []string, []AssignmentTarget, error) {
	targets, ids, _, err := normalizeAssignmentTargets(assignees)
	if err != nil {
		return nil, nil, nil, err
	}
	if s.ownerDirectory != nil {
		resolved, err := s.ownerDirectory.Resolve(ctx, ids)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, personID := range ids {
			person, ok := resolved[personID]
			if !ok {
				return nil, nil, nil, ownerdirectory.ErrSelectionInvalid
			}
			name := strings.TrimSpace(person.DisplayName)
			if name == "" {
				name = personID
			}
			departments := make([]string, 0, len(person.Organizations))
			target := targets[personID]
			selectedDepartmentID := strings.TrimSpace(target.DepartmentID)
			if selectedDepartmentID == "" {
				for _, organization := range person.Organizations {
					if organization.IsPrimary {
						selectedDepartmentID = organization.ID
						break
					}
				}
				if selectedDepartmentID == "" && len(person.Organizations) > 0 {
					selectedDepartmentID = person.Organizations[0].ID
				}
			}
			inDepartment := false
			for _, organization := range person.Organizations {
				if organization.ID == selectedDepartmentID {
					inDepartment = true
				}
				if value := strings.TrimSpace(organization.Name); value != "" {
					departments = append(departments, value)
				}
			}
			if !inDepartment {
				return nil, nil, nil, ownerdirectory.ErrSelectionInvalid
			}
			target.DepartmentID = selectedDepartmentID
			target.PersonName = name
			target.Department = strings.Join(departments, "、")
			if len([]rune(target.PersonName)) > 128 || len([]rune(target.Department)) > 128 {
				return nil, nil, nil, ownerdirectory.ErrSelectionInvalid
			}
			targets[personID] = target
		}
	}
	canonicalTargets := make([]AssignmentTarget, 0, len(ids))
	for _, personID := range ids {
		canonicalTargets = append(canonicalTargets, targets[personID])
	}
	return targets, ids, canonicalTargets, nil
}

func (s *Service) recordAssignmentEvent(ctx context.Context, actor Actor, request *PresaleRequest, assignment *Assignment, eventType, reason string, occurredAt time.Time) (*AssignmentEvent, error) {
	if request == nil || assignment == nil || assignment.ID == 0 || strings.TrimSpace(assignment.AssigneeID) == "" ||
		(eventType != AssignmentEventAdded && eventType != AssignmentEventRemoved) {
		return nil, ErrInvalidInput
	}
	eventID := AssignmentNotificationEventID(actor.TenantID, request.ID, assignment.ID, eventType)
	evidence := &AssignmentEvent{
		EventID: eventID, TenantID: actor.TenantID, RequestID: request.ID, AssignmentID: assignment.ID,
		EventType: eventType, RecipientPersonID: assignment.AssigneeID,
		RecipientNamespace: assignment.AssigneeNamespace,
		PersonNameSnapshot: assignment.AssigneeNameSnapshot, RoleSnapshot: assignment.AssigneeRole,
		ChangeReason: reason, ActorID: actor.UserID, RequestIDTrace: actor.RequestID, OccurredAt: occurredAt,
	}
	if err := s.repo.CreateAssignmentEvent(ctx, evidence); err != nil {
		return nil, err
	}
	return evidence, nil
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

func (s *Service) Assignments(ctx context.Context, actor Actor, id uint64) ([]Assignment, error) {
	if !actor.Can("presale.read") {
		return nil, ErrForbidden
	}
	requestValue, e := s.repo.FindRequest(ctx, actor.TenantID, id)
	if e != nil {
		return nil, e
	}
	if e = s.requireReadable(ctx, actor, requestValue); e != nil {
		return nil, e
	}
	return s.repo.ListAssignments(ctx, actor.TenantID, id)
}

// 详情读取与列表使用相同资源范围：管理角色可读租户内任务，销售只读本人申请，
// 被分派的基础平台用户可读其当前或历史分派。分派角色不从 OIDC 角色猜测，
// 因而猜中租户内申请 ID 也不能绕过真实业务关系。
