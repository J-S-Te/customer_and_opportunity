package presale

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

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
