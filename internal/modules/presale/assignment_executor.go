package presale

import (
	"context"
	"errors"
	"strings"
)

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
