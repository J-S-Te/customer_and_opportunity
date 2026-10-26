package presale

import (
	"context"
	"errors"
	"strings"
)

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
