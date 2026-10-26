package presale

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

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
