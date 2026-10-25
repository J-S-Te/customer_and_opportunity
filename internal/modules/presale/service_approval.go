package presale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Service) UseApprovalRuleStore(store *ApprovalRuleStore) *Service {
	s.approvalRules = store
	return s
}

// 审批任务解析器用于把本地审批节点与审批引擎当前待办进行实时绑定；
// 未配置时审批按钮和审批命令均按依赖不可用处理，而不是仅凭本地角色放行。
func (s *Service) UseApprovalTaskResolver(resolver ApprovalTaskResolver) *Service {
	s.approvalTasks = resolver
	return s
}

// 新建申请依赖独立投递 worker 的持久化心跳，而不是仅检查当前进程配置。
// 没有任何新鲜心跳时新写入失败关闭；已经提交的幂等重放仍可读取，避免重试语义被可用性检查破坏。

// 只有审批引擎确认实例创建后才推进本地状态。事件序号用于吞掉重复或乱序回执，
// 请求行和审批实例行同时加锁以缩小两个状态投影的竞争窗口。已进入待审批状态时当前实现
// 按幂等成功返回，并不会再次核对重复事件的引擎实例 ID 或事件身份。
func (s *Service) MarkApprovalStarted(ctx context.Context, tenant string, in ApprovalStartedInput) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, tenant, in.RequestID)
		if e != nil {
			return e
		}
		if r.Status == StatusPendingApproval {
			return nil
		}
		if r.Status != StatusApprovalStarting {
			return ErrInvalidTransition
		}
		inst, e := s.repo.FindApprovalInstanceForUpdate(tx, tenant, r.ID)
		if e != nil {
			return e
		}
		if in.EventSequence <= inst.LastEventSeq {
			return nil
		}
		now := approvalRoundStart(s.clock.Now())
		if e = s.repo.UpdateApprovalInstance(tx, inst, map[string]any{"engine_instance_id": in.EngineInstanceID, "status": "PENDING", "current_node": 1, "last_event_seq": in.EventSequence, "started_at": now}); e != nil {
			return e
		}
		// 通知去重键必须使用已经持久化的轮次起点，而不是另取一次时钟。
		inst.StartedAt = &now
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{"status": StatusPendingApproval, "current_approval_node": 1, "updated_by": "approval-engine"}); e != nil {
			return e
		}
		// 审批人必须从基础平台当前有效角色绑定中解析；引擎回传的展示字段不能替代
		// 当前授权校验。一个节点可能有多个有效审批人，必须逐一生成待处理通知。
		if s.notifications != nil {
			if e = s.notifyApprovalNode(tx, tenant, r, inst, 1); e != nil {
				return e
			}
		}
		return s.statusLog(tx, r, StatusApprovalStarting, StatusPendingApproval, "APPROVAL_STARTED", "", "approval-engine", "")
	})
}

// 审批命令不直接改变申请状态，而是把“当前真实待办 + 审批人 + 动作”绑定到审批实例，
// 并与 Outbox、幂等记录同事务提交。当前审批节点的角色由审批规则快照决定（approvalNodeRoleAllowedForInstance），
// 不再硬编码为特定角色；最终状态只能由携带同一任务绑定的审批引擎回调推进。

// 审批命令不直接改变申请状态，而是把“当前真实待办 + 审批人 + 动作”绑定到审批实例，
// 并与 Outbox、幂等记录同事务提交。当前审批节点的角色由审批规则快照决定（approvalNodeRoleAllowedForInstance），
// 不再硬编码为特定角色；最终状态只能由携带同一任务绑定的审批引擎回调推进。
func (s *Service) RequestApprovalAction(ctx context.Context, actor Actor, id uint64, key string, in ApprovalActionInput) error {
	if !actor.Can("presale.approve") {
		return ErrForbidden
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return ErrInvalidInput
	}
	in.Action = strings.ToUpper(strings.TrimSpace(in.Action))
	in.Comment = strings.TrimSpace(in.Comment)
	if in.Action != "PASS" && in.Action != "REJECT" {
		return ErrInvalidInput
	}
	if in.Version == 0 || len([]rune(in.Comment)) > 2000 || (in.Action == "REJECT" && in.Comment == "") {
		return ErrInvalidInput
	}
	// 角色为空的账户不可能命中任何审批节点角色，前置失败关闭（先于任何数据库查询），
	// 避免被撤销角色后仍能探测幂等重放记录。具体节点角色由 approvalNodeRoleAllowedForInstance 按规则快照动态校验。
	if len(actor.Roles) == 0 {
		return ErrForbidden
	}
	hash, err := mutationDigest(actor, id, "APPROVAL_ACTION", in.Action, struct {
		Comment string `json:"comment"`
		Version uint64 `json:"version"`
	}{in.Comment, in.Version})
	if err != nil {
		return err
	}
	replayAction := ""
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, txErr := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if txErr != nil {
			return txErr
		}
		instance, instanceErr := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, id)
		if instanceErr != nil {
			// 保持变更键查询顺序稳定，以便授权测试及
			// 重放审计，但不得因此扩大操作者的节点访问范围。
			_, _ = s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key)
			if !approvalNodeRoleAllowed(actor, r.CurrentApprovalNode) {
				return ErrForbidden
			}
			return instanceErr
		}
		currentNodeAllowed := approvalNodeRoleAllowedForInstance(actor, r.CurrentApprovalNode, instance)
		if old, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			return validateApprovalReplay(old, actor, id, in.Action, hash, instance)
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Status != StatusPendingApproval || r.Version != in.Version {
			return ErrInvalidTransition
		}
		if !currentNodeAllowed {
			return ErrForbidden
		}
		if instance.EngineInstanceID == "" || instance.CurrentNode != r.CurrentApprovalNode || instance.Status != "PENDING" {
			return ErrDependencyUnavailable
		}
		if s.approvalTasks == nil {
			replayAction = approvalReplayAction(r.CurrentApprovalNode, in.Action)
			return s.applyInternalApprovalAction(tx, actor, r, instance, key, replayAction, hash, in)
		}
		// 一个权威审批任务同一时刻只允许一个动作在途；第一条 Outbox 尚未投递完成时，新的
		// 幂等键或相反动作不能覆盖待回调绑定。
		if instance.PendingTaskID != "" || instance.PendingApprover != "" || instance.PendingAction != "" {
			return ErrInvalidTransition
		}
		resolved, resolveErr := s.approvalTasks.ResolveCurrentTask(tx, ApprovalTaskQuery{
			TenantID: actor.TenantID, EngineInstanceID: instance.EngineInstanceID,
			Node: r.CurrentApprovalNode, ApproverID: actor.UserID,
		})
		if resolveErr != nil || resolved.EngineTaskID == "" || resolved.EngineInstanceID != instance.EngineInstanceID || resolved.Node != r.CurrentApprovalNode || resolved.ApproverID != actor.UserID {
			return ErrDependencyUnavailable
		}
		replayAction = approvalReplayAction(r.CurrentApprovalNode, in.Action)
		event, eventErr := s.event(actor.TenantID, "PRESALE_APPROVAL_ACTION_REQUESTED", "presale_request", fmt.Sprint(id), map[string]any{"request_id": id, "engine_task_id": resolved.EngineTaskID, "action": in.Action, "comment": in.Comment, "actor_id": actor.UserID, "expected_version": in.Version})
		if eventErr != nil {
			return eventErr
		}
		if eventErr = s.repo.CreateOutbox(tx, event); eventErr != nil {
			return eventErr
		}
		if eventErr = s.repo.UpdateApprovalInstance(tx, instance, map[string]any{
			"pending_task_id": resolved.EngineTaskID, "pending_approver": actor.UserID, "pending_action": in.Action,
			"updated_by": actor.UserID,
		}); eventErr != nil {
			return eventErr
		}
		return s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "APPROVAL_ACTION", replayAction, hash, r.Version, s.clock.Now()))
	})
	if err == nil || errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrNotFound) {
		return err
	}
	if !isDuplicateMutationError(err) {
		return err
	}
	return s.resolveApprovalMutationRace(ctx, actor, id, key, in.Action, hash)
}

func (s *Service) applyInternalApprovalAction(ctx context.Context, actor Actor, request *PresaleRequest, instance *ApprovalInstance, key, replayAction, hash string, input ApprovalActionInput) error {
	now := s.clock.Now().UTC()
	node := request.CurrentApprovalNode
	sequence := instance.LastEventSeq + 1
	taskID := fmt.Sprintf("crm-task-%d-%d-%d", request.ID, node, request.Version)
	approverName := strings.TrimSpace(actor.UserName)
	if approverName == "" && s.ownerDirectory != nil {
		names, err := resolveOwnerDisplayNames(ctx, s.ownerDirectory, []string{actor.UserID})
		if err != nil {
			return err
		}
		approverName = names[actor.UserID]
		if approverName == "" {
			return ErrDependencyUnavailable
		}
	}
	log := &ApprovalLog{
		TenantID: actor.TenantID, RequestID: request.ID, Node: node,
		ApproverID: actor.UserID, ApproverNameSnapshot: approverName,
		Result: input.Action, Comment: input.Comment, ApprovedAt: now,
		EngineTaskID: taskID, EngineInstanceID: instance.EngineInstanceID,
		EventSequence: sequence, RequestIDTrace: actor.RequestID,
	}
	if err := s.repo.CreateApprovalLog(ctx, log); err != nil {
		return err
	}
	requestFields := map[string]any{"updated_by": actor.UserID}
	instanceFields := map[string]any{"last_event_seq": sequence, "updated_by": actor.UserID}
	to := StatusPendingApproval
	if input.Action == "REJECT" {
		to = StatusRejected
		requestFields["status"] = to
		requestFields["reject_reason"] = input.Comment
		requestFields["current_approval_node"] = 0
		instanceFields["status"] = "REJECTED"
		instanceFields["finished_at"] = now
	} else if nextNode, ok := nextApprovalNode(instance, node); ok {
		requestFields["current_approval_node"] = nextNode
		instanceFields["current_node"] = nextNode
		if s.notifications != nil {
			if err := s.notifyApprovalNode(ctx, actor.TenantID, request, instance, nextNode); err != nil {
				return err
			}
		}
	} else {
		to = StatusApprovedPendingAssignment
		requestFields["status"] = to
		requestFields["current_approval_node"] = 0
		instanceFields["status"] = "APPROVED"
		instanceFields["finished_at"] = now
	}
	if err := s.repo.UpdateApprovalInstance(ctx, instance, instanceFields); err != nil {
		return err
	}
	if err := s.repo.UpdateRequestVersioned(ctx, request, request.Version, requestFields); err != nil {
		return err
	}
	if to == StatusApprovedPendingAssignment {
		if err := s.notifyWorkflow(ctx, WorkflowNotification{
			TenantID: actor.TenantID, RecipientID: request.ApplicantID,
			Type: "PRESALE_APPROVAL_APPROVED", Title: "售前审批已通过",
			Body: "您的售前申请已完成审批。", RequestID: request.ID, RequestNo: request.RequestNo,
			SourceDiscriminator: approvalRoundDiscriminator(instance),
		}); err != nil {
			return err
		}
	}
	if to == StatusRejected {
		if err := s.notifyWorkflow(ctx, WorkflowNotification{
			TenantID: actor.TenantID, RecipientID: request.ApplicantID,
			Type: "PRESALE_APPROVAL_REJECTED", Title: "售前审批已驳回",
			Body: "您的售前申请已被驳回：" + input.Comment, RequestID: request.ID, RequestNo: request.RequestNo,
			SourceDiscriminator: approvalRoundDiscriminator(instance),
		}); err != nil {
			return err
		}
	}
	if err := s.repo.CreateMutationReplay(ctx, newMutationReplay(actor, request.ID, key, "APPROVAL_ACTION", replayAction, hash, input.Version, now)); err != nil {
		return err
	}
	if to != StatusPendingApproval {
		return s.statusLog(ctx, request, StatusPendingApproval, to, "APPROVAL_INTERNAL", input.Comment, actor.UserID, actor.RequestID)
	}
	return nil
}

// 审批回调以引擎任务 ID 做事实级去重，并同时核验实例、事件序号、当前节点和待处理绑定。
// 第一级通过只切换到第二节点；第二级通过进入待分派；任一级驳回都进入终态。
// 日志、审批实例和申请状态在同一事务更新，乱序、陈旧或被篡改的事件统一拒绝。

// 审批回调以引擎任务 ID 做事实级去重，并同时核验实例、事件序号、当前节点和待处理绑定。
// 第一级通过只切换到第二节点；第二级通过进入待分派；任一级驳回都进入终态。
// 日志、审批实例和申请状态在同一事务更新，乱序、陈旧或被篡改的事件统一拒绝。
func (s *Service) HandleApprovalCallback(ctx context.Context, tenant string, in ApprovalCallbackInput) error {
	in.EngineInstanceID = strings.TrimSpace(in.EngineInstanceID)
	in.EngineTaskID = strings.TrimSpace(in.EngineTaskID)
	in.ApproverID = strings.TrimSpace(in.ApproverID)
	in.ApproverName = strings.TrimSpace(in.ApproverName)
	in.NextApproverID = strings.TrimSpace(in.NextApproverID)
	in.NextApproverName = strings.TrimSpace(in.NextApproverName)
	in.Result = strings.ToUpper(strings.TrimSpace(in.Result))
	in.Comment = strings.TrimSpace(in.Comment)
	if in.RequestID == 0 || in.EventSequence == 0 || in.OccurredAt.IsZero() || in.Node < 1 || in.Node > 10 ||
		!validApprovalTaskIdentity(in.EngineInstanceID) || !validApprovalTaskIdentity(in.EngineTaskID) || !validApprovalTaskIdentity(in.ApproverID) ||
		in.ApproverName == "" || len([]rune(in.ApproverName)) > 128 || len([]rune(in.Comment)) > 2000 ||
		(in.NextApproverID != "" && (!validApprovalTaskIdentity(in.NextApproverID) || in.NextApproverName == "" || len([]rune(in.NextApproverName)) > 128)) ||
		(in.Result != "PASS" && in.Result != "REJECT") || (in.Result == "REJECT" && in.Comment == "") {
		return ErrInvalidApprovalEvent
	}
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if old, e := s.repo.FindEngineTaskLog(tx, tenant, in.EngineTaskID); e == nil {
			if old.RequestID != in.RequestID || old.EngineInstanceID != in.EngineInstanceID || old.EventSequence != in.EventSequence ||
				old.Node != in.Node || old.ApproverID != in.ApproverID || old.ApproverNameSnapshot != in.ApproverName || old.Result != in.Result ||
				old.Comment != in.Comment || !old.ApprovedAt.Equal(in.OccurredAt.UTC()) {
				return ErrInvalidApprovalEvent
			}
			return nil
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		r, e := s.repo.FindRequestForUpdate(tx, tenant, in.RequestID)
		if e != nil {
			return e
		}
		inst, e := s.repo.FindApprovalInstanceForUpdate(tx, tenant, r.ID)
		if e != nil {
			return e
		}
		if inst.EngineInstanceID != in.EngineInstanceID || in.EventSequence <= inst.LastEventSeq || r.Status != StatusPendingApproval || r.CurrentApprovalNode != in.Node ||
			inst.PendingTaskID == "" || inst.PendingTaskID != in.EngineTaskID || inst.PendingApprover == "" || inst.PendingApprover != in.ApproverID || inst.PendingAction != in.Result {
			return ErrInvalidApprovalEvent
		}
		log := &ApprovalLog{TenantID: tenant, RequestID: r.ID, Node: in.Node, ApproverID: in.ApproverID, ApproverNameSnapshot: in.ApproverName, Result: in.Result, Comment: in.Comment, ApprovedAt: in.OccurredAt.UTC(), EngineTaskID: in.EngineTaskID, EngineInstanceID: in.EngineInstanceID, EventSequence: in.EventSequence}
		if e = s.repo.CreateApprovalLog(tx, log); e != nil {
			return e
		}
		from := r.Status
		fields := map[string]any{"updated_by": "approval-engine"}
		instFields := map[string]any{"last_event_seq": in.EventSequence, "pending_task_id": "", "pending_approver": "", "pending_action": ""}
		if in.Result == "REJECT" {
			fields["status"] = StatusRejected
			fields["reject_reason"] = in.Comment
			fields["current_approval_node"] = 0
			instFields["status"] = "REJECTED"
			instFields["finished_at"] = in.OccurredAt.UTC()
		} else if nextNode, ok := nextApprovalNode(inst, in.Node); ok {
			fields["current_approval_node"] = nextNode
			instFields["current_node"] = nextNode
		} else {
			fields["status"] = StatusApprovedPendingAssignment
			fields["current_approval_node"] = 0
			instFields["status"] = "APPROVED"
			instFields["finished_at"] = in.OccurredAt.UTC()
		}
		if e = s.repo.UpdateApprovalInstance(tx, inst, instFields); e != nil {
			return e
		}
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, fields); e != nil {
			return e
		}
		to := StatusPendingApproval
		if in.Result == "REJECT" {
			to = StatusRejected
		} else if _, hasNext := nextApprovalNode(inst, in.Node); !hasNext {
			to = StatusApprovedPendingAssignment
		}
		if in.Result == "PASS" && to == StatusPendingApproval && s.notifications != nil {
			nextNode, hasNext := nextApprovalNode(inst, in.Node)
			if !hasNext {
				return ErrDependencyUnavailable
			}
			if e = s.notifyApprovalNode(tx, tenant, r, inst, nextNode); e != nil {
				return e
			}
		} else if in.Result == "PASS" && to != from {
			if e = s.notifyWorkflow(tx, WorkflowNotification{TenantID: tenant, RecipientID: r.ApplicantID, Type: "PRESALE_APPROVAL_APPROVED", Title: "售前审批已通过", Body: "您的售前申请已完成审批。", RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: approvalRoundDiscriminator(inst)}); e != nil {
				return e
			}
		}
		if in.Result == "REJECT" {
			if e = s.notifyWorkflow(tx, WorkflowNotification{TenantID: tenant, RecipientID: r.ApplicantID, Type: "PRESALE_APPROVAL_REJECTED", Title: "售前审批已驳回", Body: "您的售前申请已被驳回：" + in.Comment, RequestID: r.ID, RequestNo: r.RequestNo, SourceDiscriminator: approvalRoundDiscriminator(inst)}); e != nil {
				return e
			}
		}
		if to != from {
			return s.statusLog(tx, r, from, to, "APPROVAL_CALLBACK", in.Comment, in.ApproverID, log.RequestIDTrace)
		}
		return nil
	})
}

// 分派采用“目标集合替换”语义：保留集合不重复写入，移除项结束历史关系，执行人来自
// 基础平台授权人员选择器。首次有效分派会把申请推进到 EXECUTING；执行中
// 调整人员后还会重新判断所有当前执行人是否已有有效工时，从而可能立即完成任务。

const approvalRecipientPageSize = 50

// notifyApprovalNode 解析当前绑定节点角色的全部有效用户，并为每个
// 唯一接收者写入一条通知。角色为空、目录不可用或结果为空时，
// 外层事务失败，确保流程不能在没有可达审批人的情况下推进。

func approvalRoleForNode(instance *ApprovalInstance, node uint8) (string, bool) {
	if node == 0 {
		return "", false
	}
	if instance == nil || len(instance.NodesJSON) == 0 {
		switch node {
		case 1:
			return "sales_director", true
		case 2:
			return "technical_director", true
		default:
			return "", false
		}
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil || int(node) > len(nodes) {
		return "", false
	}
	current := nodes[node-1]
	role := strings.TrimSpace(current.RoleCode)
	return role, current.Type == ApprovalNodeApproval && role != ""
}

func approvalNodeRoleAllowed(actor Actor, node uint8) bool {
	if actor.HasRole("crm_super_admin") {
		return node >= 1 && node <= 10
	}
	return node == 1 && (actor.HasRole("sales_director") || actor.HasRole("crm_super_admin")) || node == 2 && (actor.HasRole("technical_director") || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin"))
}

func nextApprovalNode(instance *ApprovalInstance, node uint8) (uint8, bool) {
	if instance == nil || len(instance.NodesJSON) == 0 {
		return 2, node == 1
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil || len(nodes) == 0 {
		return 2, node == 1
	}
	for index := int(node); index < len(nodes); index++ {
		if nodes[index].Type == ApprovalNodeApproval {
			return uint8(index + 1), true
		}
	}
	return 0, false
}

// assignmentActionAllowed 是历史单层指派动作的唯一运行时
// 门禁。审批规则以快照形式保存在实例上，因此
// 规则变化只影响新请求，不能在执行中途改变
// 有效工作流。

func approvalNodeRoleAllowedForInstance(actor Actor, node uint8, instance *ApprovalInstance) bool {
	// CRM 超级管理员继承所有审批节点的处理权限，不受规则节点 role_code 限制。
	if actor.HasRole("crm_super_admin") {
		return true
	}
	if instance == nil || len(instance.NodesJSON) == 0 {
		return approvalNodeRoleAllowed(actor, node)
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil || node == 0 || int(node) > len(nodes) {
		return false
	}
	current := nodes[node-1]
	if current.Type != ApprovalNodeApproval {
		return false
	}
	return actor.HasRole(strings.TrimSpace(current.RoleCode))
}

func (s *Service) ApprovalHistory(ctx context.Context, actor Actor, id uint64) ([]ApprovalLog, error) {
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
	return s.repo.ListApprovalLogs(ctx, actor.TenantID, id)
}
