package presale

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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
