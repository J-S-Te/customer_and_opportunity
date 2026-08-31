package presale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"gorm.io/gorm"
)

const completionPolicy = "ALL_CURRENT_ASSIGNEES_HAVE_WORKLOG"

var decimalPattern = regexp.MustCompile(`^(?:0|[1-9]\d{0,7})(?:\.\d{1,2})?$`)

type Service struct {
	repo              Repository
	opportunities     OpportunityReader
	phones            PhoneProtector
	clock             Clock
	ids               IDGenerator
	dayHours          string
	timelineCursorKey []byte
	approvalTasks     ApprovalTaskResolver
	auditWriter       audit.Writer
	workerReadiness   WorkerReadiness
	workerMaxAge      time.Duration
	ownerDirectory    ownerdirectory.Catalog
	approvalRules     *ApprovalRuleStore
	notifications     WorkflowNotificationWriter
}

func (s *Service) UseApprovalRuleStore(store *ApprovalRuleStore) *Service {
	s.approvalRules = store
	return s
}

func (s *Service) UseWorkflowNotifications(writer WorkflowNotificationWriter) *Service {
	s.notifications = writer
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
func (s *Service) UseWorkerReadiness(readiness WorkerReadiness, maxAge time.Duration) *Service {
	s.workerReadiness = readiness
	s.workerMaxAge = maxAge
	return s
}

// 联系电话明文返回前必须先写入隐私审计；审计存储不可用时敏感数据接口失败关闭。
func (s *Service) UseAuditWriter(writer audit.Writer) *Service {
	s.auditWriter = writer
	return s
}

// UseOwnerDirectory 使执行人指派与基础平台当前有效授权目录保持一致。
// 服务端会重新解析用户姓名和组织，不信任浏览器提交的快照字段。
func (s *Service) UseOwnerDirectory(catalog ownerdirectory.Catalog) *Service {
	s.ownerDirectory = catalog
	return s
}

func NewService(repo Repository, opportunities OpportunityReader, phones PhoneProtector, clock Clock, ids IDGenerator) *Service {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Service{repo: repo, opportunities: opportunities, phones: phones, clock: clock, ids: ids, dayHours: "8.00"}
}

// 创建申请先校验商机的数据范围，再检查租户级幂等键，避免猜测幂等键泄露他人申请。
// 审批属于 CRM 内部能力，申请与本地审批实例在同一事务进入待审批状态。
func (s *Service) CreateRequest(ctx context.Context, actor Actor, key string, in CreateRequestInput) (*PresaleRequest, error) {
	if !actor.Can("presale.create") {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return nil, ErrInvalidInput
	}
	if err := validateCreate(in); err != nil {
		return nil, err
	}
	opp, err := s.opportunities.GetAccessible(ctx, actor, in.OpportunityID)
	if err != nil {
		return nil, err
	}
	applicantName := strings.TrimSpace(actor.UserName)
	if applicantName == "" && s.ownerDirectory != nil {
		names, resolveErr := resolveOwnerDisplayNames(ctx, s.ownerDirectory, []string{actor.UserID})
		if resolveErr != nil {
			return nil, resolveErr
		}
		applicantName = names[actor.UserID]
		if applicantName == "" {
			return nil, ErrDependencyUnavailable
		}
	}
	hash, legacyHash, err := createRequestHashes(actor, in)
	if err != nil {
		return nil, err
	}
	// 先确认目标商机处于调用者的数据范围，再查询租户级幂等键；否则猜中他人的键值可能
	// 暴露由其他销售创建或属于不可见商机的申请。
	if old, findErr := s.repo.FindRequestByCreateKey(ctx, actor.TenantID, key); findErr == nil {
		if !sameCreateRequestReplay(old, actor, opp.ID, hash, legacyHash) {
			return nil, ErrIdempotencyConflict
		}
		return old, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	cipher, err := s.phones.Encrypt(ctx, in.ContactPhone)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	var matchedRule *ApprovalRule
	if s.approvalRules != nil {
		rules, listErr := s.approvalRules.List(ctx, actor.TenantID, true)
		if listErr != nil {
			return nil, listErr
		}
		matchedRule, err = MatchHighestApprovalRule(rules, ApprovalFacts{Urgency: string(in.Urgency), Venue: string(in.Venue), OpportunityID: opp.ID})
		if err != nil {
			return nil, err
		}
	}
	var created *PresaleRequest
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if old, e := s.repo.FindRequestByCreateKey(tx, actor.TenantID, key); e == nil {
			if !sameCreateRequestReplay(old, actor, opp.ID, hash, legacyHash) {
				return ErrIdempotencyConflict
			}
			created = old
			return nil
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		no, e := s.repo.NextRequestNo(tx, actor.TenantID, now)
		if e != nil {
			return e
		}
		r := &PresaleRequest{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestNo: no, OpportunityID: opp.ID, OpportunityNoSnapshot: opp.OpportunityNo, ApplicantID: actor.UserID, ApplicantNameSnapshot: applicantName, Venue: in.Venue, ServiceAddress: strings.TrimSpace(in.ServiceAddress), ContactName: strings.TrimSpace(in.ContactName), ContactPhoneCipher: cipher, ContactPhoneMasked: s.phones.Mask(in.ContactPhone), Description: strings.TrimSpace(in.Description), ExpectedStart: in.ExpectedStart.UTC(), ExpectedEnd: in.ExpectedEnd.UTC(), Urgency: in.Urgency, Status: StatusPendingApproval, CurrentApprovalNode: 1, CreateIdempotencyKey: key, CreateRequestHash: hash}
		if e = s.repo.CreateRequest(tx, r); e != nil {
			return e
		}
		instanceID := fmt.Sprintf("crm-presale-%d", r.ID)
		inst := &ApprovalInstance{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestID: r.ID, EngineInstanceID: instanceID, Status: "PENDING", CurrentNode: 1, StartedAt: &now}
		if matchedRule != nil {
			inst.RuleID, inst.RuleVersion = matchedRule.ID, matchedRule.Version
			inst.NodesJSON, _ = json.Marshal(matchedRule.Nodes)
		}
		if e = s.repo.CreateApprovalInstance(tx, inst); e != nil {
			return e
		}
		if s.notifications != nil {
			if e = s.notifyApprovalNode(tx, actor.TenantID, r, inst, 1); e != nil {
				return e
			}
		}
		created = r
		return nil
	})
	if err != nil {
		// 不同商机的申请没有共享父行锁，可能并发争用同一个租户级幂等键。唯一键决定胜者后，
		// 仍按操作者、商机和摘要重新判定重放或冲突，不能返回数据库重复键或他人的资源。
		if old, findErr := s.repo.FindRequestByCreateKey(ctx, actor.TenantID, key); findErr == nil {
			if !sameCreateRequestReplay(old, actor, opp.ID, hash, legacyHash) {
				return nil, ErrIdempotencyConflict
			}
			return old, nil
		}
	}
	return created, err
}

func (s *Service) deliveryWorkerReady(ctx context.Context) bool {
	if s.workerReadiness == nil || s.workerMaxAge <= 0 {
		return false
	}
	ready, err := s.workerReadiness.HasFreshHeartbeat(ctx, PresaleDeliveryWorkerType, s.clock.Now().Add(-s.workerMaxAge))
	return err == nil && ready
}

// ReopenRequest reuses the existing request and approval-instance IDs. Only the
// terminal rejected/cancelled request can be reopened; its history remains intact.
func (s *Service) ReopenRequest(ctx context.Context, actor Actor, id uint64, version uint64, in ReopenRequestInput) (*PresaleRequest, error) {
	if !actor.Can("presale.create") {
		return nil, ErrForbidden
	}
	if err := validateCreate(CreateRequestInput{
		OpportunityID: 1, Venue: in.Venue, ServiceAddress: in.ServiceAddress,
		ContactName: in.ContactName, ContactPhone: in.ContactPhone,
		Description: in.Description, ExpectedStart: in.ExpectedStart,
		ExpectedEnd: in.ExpectedEnd, Urgency: in.Urgency,
	}); err != nil {
		return nil, err
	}
	cipher, err := s.phones.Encrypt(ctx, strings.TrimSpace(in.ContactPhone))
	if err != nil {
		return nil, err
	}
	var reopened *PresaleRequest
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, err := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if err != nil {
			return err
		}
		if r.Status != StatusRejected && r.Status != StatusCancelled || r.Version != version {
			return ErrInvalidTransition
		}
		if r.ApplicantID != actor.UserID && !actor.HasRole("crm_super_admin") {
			return ErrForbidden
		}
		previousStatus := r.Status
		inst, err := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, id)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{
			"status": StatusPendingApproval, "current_approval_node": 1,
			"reject_reason": "", "cancelled_at": nil, "updated_by": actor.UserID,
			"venue": in.Venue, "service_address": strings.TrimSpace(in.ServiceAddress),
			"contact_name": strings.TrimSpace(in.ContactName), "contact_phone_cipher": cipher,
			"contact_phone_masked": s.phones.Mask(strings.TrimSpace(in.ContactPhone)),
			"description":          strings.TrimSpace(in.Description),
			"expected_start":       in.ExpectedStart.UTC(), "expected_end": in.ExpectedEnd.UTC(),
			"urgency": in.Urgency,
		}); err != nil {
			return err
		}
		r.Status = StatusPendingApproval
		r.CurrentApprovalNode = 1
		r.RejectReason = ""
		r.CancelledAt = nil
		r.UpdatedBy = actor.UserID
		r.Venue = in.Venue
		r.ServiceAddress = strings.TrimSpace(in.ServiceAddress)
		r.ContactName = strings.TrimSpace(in.ContactName)
		r.ContactPhoneCipher = cipher
		r.ContactPhoneMasked = s.phones.Mask(strings.TrimSpace(in.ContactPhone))
		r.Description = strings.TrimSpace(in.Description)
		r.ExpectedStart = in.ExpectedStart.UTC()
		r.ExpectedEnd = in.ExpectedEnd.UTC()
		r.Urgency = in.Urgency
		if err = s.repo.UpdateApprovalInstance(tx, inst, map[string]any{
			"status": "PENDING", "current_node": 1, "pending_task_id": "", "pending_approver": "", "pending_action": "",
			"finished_at": nil, "started_at": &now, "updated_by": actor.UserID,
		}); err != nil {
			return err
		}
		if s.notifications != nil {
			if err = s.notifyApprovalNode(tx, actor.TenantID, r, inst, 1); err != nil {
				return err
			}
		}
		if err = s.statusLog(tx, r, previousStatus, StatusPendingApproval, "REOPENED", "", actor.UserID, actor.RequestID); err != nil {
			return err
		}
		reopened = r
		return nil
	})
	return reopened, err
}

func createRequestHashes(actor Actor, in CreateRequestInput) (string, string, error) {
	canonical := struct {
		ActorID        string    `json:"actor_id"`
		OpportunityID  uint64    `json:"opportunity_id"`
		Venue          Venue     `json:"venue"`
		ServiceAddress string    `json:"service_address"`
		ContactName    string    `json:"contact_name"`
		ContactPhone   string    `json:"contact_phone"`
		Description    string    `json:"description"`
		ExpectedStart  time.Time `json:"expected_start"`
		ExpectedEnd    time.Time `json:"expected_end"`
		Urgency        Urgency   `json:"urgency"`
	}{
		ActorID: actor.UserID, OpportunityID: in.OpportunityID, Venue: in.Venue,
		ServiceAddress: strings.TrimSpace(in.ServiceAddress), ContactName: strings.TrimSpace(in.ContactName),
		ContactPhone: strings.TrimSpace(in.ContactPhone), Description: strings.TrimSpace(in.Description),
		ExpectedStart: in.ExpectedStart.UTC(), ExpectedEnd: in.ExpectedEnd.UTC(), Urgency: in.Urgency,
	}
	hash, err := requestDigest(canonical)
	if err != nil {
		return "", "", err
	}
	// 旧版摘要只在持久化操作者和父商机也精确匹配时接受，用于兼容滚动发布期间的安全重试；
	// 历史版本仅绑定请求体，不能单独作为跨操作者或跨商机重放的依据。
	legacyHash, err := requestDigest(in)
	return hash, legacyHash, err
}

func sameCreateRequestReplay(old *PresaleRequest, actor Actor, opportunityID uint64, hash, legacyHash string) bool {
	if old == nil || old.OpportunityID != opportunityID || old.ApplicantID != actor.UserID || old.CreatedBy != actor.UserID {
		return false
	}
	return old.CreateRequestHash == hash || old.CreateRequestHash == legacyHash
}

func validateCreate(in CreateRequestInput) error {
	if in.OpportunityID == 0 || (in.Venue != VenueOnsite && in.Venue != VenueRemote) || (in.Urgency != UrgencyNormal && in.Urgency != UrgencyUrgent) {
		return ErrInvalidInput
	}
	if in.Venue == VenueOnsite && strings.TrimSpace(in.ServiceAddress) == "" {
		return ErrInvalidInput
	}
	if strings.TrimSpace(in.ContactName) == "" || strings.TrimSpace(in.ContactPhone) == "" {
		return ErrInvalidInput
	}
	if strings.TrimSpace(in.Description) == "" || in.ExpectedEnd.Before(in.ExpectedStart) {
		return ErrInvalidInput
	}
	return nil
}

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
		now := s.clock.Now()
		if e = s.repo.UpdateApprovalInstance(tx, inst, map[string]any{"engine_instance_id": in.EngineInstanceID, "status": "PENDING", "current_node": 1, "last_event_seq": in.EventSequence, "started_at": now}); e != nil {
			return e
		}
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
			// Keep the mutation-key lookup ordering stable for authorization tests and
			// replay auditing, but never allow it to expand the actor's node access.
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
		}); err != nil {
			return err
		}
	}
	if to == StatusRejected {
		if err := s.notifyWorkflow(ctx, WorkflowNotification{
			TenantID: actor.TenantID, RecipientID: request.ApplicantID,
			Type: "PRESALE_APPROVAL_REJECTED", Title: "售前审批已驳回",
			Body: "您的售前申请已被驳回：" + input.Comment, RequestID: request.ID, RequestNo: request.RequestNo,
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
			if e = s.notifyWorkflow(tx, WorkflowNotification{TenantID: tenant, RecipientID: r.ApplicantID, Type: "PRESALE_APPROVAL_APPROVED", Title: "售前审批已通过", Body: "您的售前申请已完成审批。", RequestID: r.ID, RequestNo: r.RequestNo}); e != nil {
				return e
			}
		}
		if in.Result == "REJECT" {
			if e = s.notifyWorkflow(tx, WorkflowNotification{TenantID: tenant, RecipientID: r.ApplicantID, Type: "PRESALE_APPROVAL_REJECTED", Title: "售前审批已驳回", Body: "您的售前申请已被驳回：" + in.Comment, RequestID: r.ID, RequestNo: r.RequestNo}); e != nil {
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
func (s *Service) ReplaceAssignments(ctx context.Context, actor Actor, id uint64, key string, in ReplaceAssignmentsInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	in.ChangeReason = strings.TrimSpace(in.ChangeReason)
	if key == "" || len(key) > 128 || len(in.Assignees) == 0 || len(in.Assignees) > 100 || len([]rune(in.ChangeReason)) > 1000 {
		return nil, ErrInvalidInput
	}
	targets := map[string]AssignmentTarget{}
	// 执行岗位明确区分项目经理与技术人员。implementation_engineer 仅保留给历史指派记录，
	// 新指派统一使用 technician，不能再把负责人或总监作为执行人员写入。
	allowedRoles := map[string]bool{"team_lead": true, "project_manager": true, "technician": true}
	for _, v := range in.Assignees {
		if strings.TrimSpace(v.PersonID) == "" || !allowedRoles[v.Role] || len([]rune(v.PersonName)) > 128 || len([]rune(v.Department)) > 128 || len([]rune(v.DepartmentID)) > 64 {
			return nil, ErrInvalidInput
		}
		v.PersonID = strings.TrimSpace(v.PersonID)
		v.PersonName = strings.TrimSpace(v.PersonName)
		v.Department = strings.TrimSpace(v.Department)
		v.DepartmentID = strings.TrimSpace(v.DepartmentID)
		v.Role = strings.TrimSpace(v.Role)
		targets[v.PersonID] = v
	}
	if len(targets) != len(in.Assignees) {
		return nil, ErrInvalidInput
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if s.ownerDirectory != nil {
		resolved, err := s.ownerDirectory.Resolve(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, personID := range ids {
			person, ok := resolved[personID]
			if !ok {
				return nil, ownerdirectory.ErrSelectionInvalid
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
				return nil, ownerdirectory.ErrSelectionInvalid
			}
			target.DepartmentID = selectedDepartmentID
			target.PersonName = name
			target.Department = strings.Join(departments, "、")
			if len([]rune(target.PersonName)) > 128 || len([]rune(target.Department)) > 128 {
				return nil, ownerdirectory.ErrSelectionInvalid
			}
			targets[personID] = target
		}
	}
	canonicalTargets := make([]AssignmentTarget, 0, len(ids))
	for _, personID := range ids {
		canonicalTargets = append(canonicalTargets, targets[personID])
	}
	hash, e := mutationDigest(actor, id, "REPLACE_ASSIGNMENTS", "REPLACE", struct {
		Assignees    []AssignmentTarget `json:"assignees"`
		ChangeReason string             `json:"change_reason"`
		Version      uint64             `json:"version"`
	}{canonicalTargets, in.ChangeReason, in.Version})
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
			a := &Assignment{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestID: id, AssigneeID: pid, AssigneeNameSnapshot: personName, AssigneeDepartmentSnapshot: t.Department, AssigneeRole: t.Role, AssignedBy: actor.UserID, AssignedAt: now, IsCurrent: true, BatchNo: batch, ChangeReason: in.ChangeReason}
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

func (s *Service) SelectExecutionDepartment(ctx context.Context, actor Actor, id uint64, in SelectDepartmentInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") {
		return nil, ErrForbidden
	}
	in.DepartmentID = strings.TrimSpace(in.DepartmentID)
	if in.DepartmentID == "" || in.Version == 0 {
		return nil, ErrInvalidInput
	}
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

func (s *Service) notifyWorkflow(ctx context.Context, n WorkflowNotification) error {
	if s.notifications == nil {
		return nil
	}
	return s.notifications.Write(ctx, n)
}

const approvalRecipientPageSize = 50

// notifyApprovalNode resolves every currently active user bound to the node role and writes one
// notification per unique recipient. An empty role, unavailable directory, or empty result fails
// the surrounding transaction so a workflow can never advance without a reachable approver.
func (s *Service) notifyApprovalNode(ctx context.Context, tenant string, request *PresaleRequest, instance *ApprovalInstance, node uint8) error {
	role, ok := approvalRoleForNode(instance, node)
	if !ok || s.ownerDirectory == nil || request == nil {
		return ErrDependencyUnavailable
	}
	recipients := make([]ownerdirectory.User, 0)
	seen := make(map[string]struct{})
	for pageNumber := 1; pageNumber <= ownerDirectoryMaxPages; pageNumber++ {
		page, err := s.ownerDirectory.List(ctx, ownerdirectory.Query{RoleCodes: []string{role}, Page: pageNumber, PageSize: approvalRecipientPageSize})
		if err != nil {
			return ErrDependencyUnavailable
		}
		for _, user := range page.Items {
			user.ID = strings.TrimSpace(user.ID)
			if user.ID == "" {
				continue
			}
			if _, exists := seen[user.ID]; exists {
				continue
			}
			seen[user.ID] = struct{}{}
			recipients = append(recipients, user)
		}
		if len(page.Items) == 0 || int64(len(recipients)) >= page.Total || len(page.Items) < approvalRecipientPageSize {
			break
		}
	}
	if len(recipients) == 0 {
		return ErrDependencyUnavailable
	}
	for _, recipient := range recipients {
		body := "售前申请已流转到您当前审批节点，请及时处理。"
		if name := strings.TrimSpace(recipient.DisplayName); name != "" {
			body = name + "，" + body
		}
		if err := s.notifyWorkflow(ctx, WorkflowNotification{TenantID: tenant, RecipientID: recipient.ID, Type: "PRESALE_APPROVAL_PENDING", Title: "售前审批待处理", Body: body, RequestID: request.ID, RequestNo: request.RequestNo, ApprovalNode: node}); err != nil {
			return err
		}
	}
	return nil
}

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

const assignmentNotificationEventType = "PRESALE_ASSIGNMENT_SITE_NOTIFICATION"
const ProgressNotificationOutboxEventType = "PRESALE_PROGRESS_SITE_NOTIFICATION"

func (s *Service) recordAssignmentNotification(ctx context.Context, actor Actor, request *PresaleRequest, assignment *Assignment, eventType, reason string, occurredAt time.Time) error {
	if request == nil || assignment == nil || assignment.ID == 0 || strings.TrimSpace(assignment.AssigneeID) == "" ||
		(eventType != AssignmentEventAdded && eventType != AssignmentEventRemoved) {
		return ErrInvalidInput
	}
	eventID := AssignmentNotificationEventID(actor.TenantID, request.ID, assignment.ID, eventType)
	evidence := &AssignmentEvent{
		EventID: eventID, TenantID: actor.TenantID, RequestID: request.ID, AssignmentID: assignment.ID,
		EventType: eventType, RecipientPersonID: assignment.AssigneeID,
		PersonNameSnapshot: assignment.AssigneeNameSnapshot, RoleSnapshot: assignment.AssigneeRole,
		ChangeReason: reason, ActorID: actor.UserID, RequestIDTrace: actor.RequestID, OccurredAt: occurredAt,
	}
	if err := s.repo.CreateAssignmentEvent(ctx, evidence); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		AssignmentEventID uint64 `json:"assignment_event_id"`
	}{AssignmentEventID: evidence.ID})
	if err != nil {
		return err
	}
	return s.repo.CreateOutbox(ctx, &OutboxEvent{
		EventID: eventID, TenantID: actor.TenantID, EventType: assignmentNotificationEventType,
		AggregateType: "presale_assignment_event", AggregateID: fmt.Sprint(evidence.ID), Payload: payload,
		Status: "PENDING", CreatedAt: occurredAt,
	})
}

// 事件 ID 由租户、申请、任职记录和变更类型共同决定，生产者与通知投影使用同一算法，
// 因此 worker 重试不会为同一次人员变更重复创建通知。
func AssignmentNotificationEventID(tenantID string, requestID, assignmentID uint64, eventType string) string {
	value := tenantID + "\x00" + fmt.Sprint(requestID) + "\x00" + fmt.Sprint(assignmentID) + "\x00" + eventType
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// 进展通知 ID 同时绑定不可变进展记录、分派记录、收件人命名空间和身份，
// 保证同一收件人的重试可去重，同时兼容存量事件的命名空间。
func ProgressNotificationEventID(tenantID string, requestID, progressID, assignmentID uint64, namespace, recipientID, kind string) string {
	value := strings.Join([]string{tenantID, fmt.Sprint(requestID), fmt.Sprint(progressID), fmt.Sprint(assignmentID), namespace, recipientID, kind}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Service) recordProgressNotifications(ctx context.Context, actor Actor, request *PresaleRequest, progress *ProgressLog, assignments []Assignment) error {
	if request == nil || progress == nil || progress.ID == 0 || actor.UserID == "" || actor.PersonID == "" {
		return ErrInvalidInput
	}
	type recipient struct {
		id, namespace, kind string
		assignmentID        uint64
	}
	recipients := make([]recipient, 0, len(assignments)+1)
	seenPersonRecipients := make(map[string]struct{}, len(assignments))
	// 申请人属于 CRM 用户命名空间，只有作者的 USER 身份相等时才排除其自通知。
	if request.ApplicantID != "" && request.ApplicantID != actor.UserID {
		recipients = append(recipients, recipient{id: request.ApplicantID, namespace: ProgressRecipientUser, kind: ProgressRecipientApplicant})
	}
	for _, assignment := range assignments {
		// 新执行人使用基础平台 user_id；PersonID 字段名仅为数据库兼容保留。
		if !assignment.IsCurrent || assignment.ID == 0 || assignment.AssigneeID == "" || assignment.AssigneeID == actor.PersonID {
			continue
		}
		if _, duplicate := seenPersonRecipients[assignment.AssigneeID]; duplicate {
			continue
		}
		seenPersonRecipients[assignment.AssigneeID] = struct{}{}
		recipients = append(recipients, recipient{id: assignment.AssigneeID, namespace: ProgressRecipientPerson, kind: ProgressRecipientAssignee, assignmentID: assignment.ID})
	}
	for _, value := range recipients {
		eventID := ProgressNotificationEventID(actor.TenantID, request.ID, progress.ID, value.assignmentID, value.namespace, value.id, value.kind)
		evidence := &ProgressNotificationEvent{
			EventID: eventID, TenantID: actor.TenantID, RequestID: request.ID, ProgressID: progress.ID,
			AssignmentID: value.assignmentID, RecipientID: value.id, RecipientNamespace: value.namespace,
			RecipientKind: value.kind, AuthorUserID: actor.UserID, AuthorPersonID: actor.PersonID,
			RequestIDTrace: actor.RequestID, OccurredAt: progress.CreatedAt,
		}
		if err := s.repo.CreateProgressNotificationEvent(ctx, evidence); err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			ProgressNotificationEventID uint64 `json:"progress_notification_event_id"`
		}{ProgressNotificationEventID: evidence.ID})
		if err != nil {
			return err
		}
		if err = s.repo.CreateOutbox(ctx, &OutboxEvent{
			EventID: eventID, TenantID: actor.TenantID, EventType: ProgressNotificationOutboxEventType,
			AggregateType: "presale_progress_notification_event", AggregateID: fmt.Sprint(evidence.ID),
			Payload: payload, Status: "PENDING", CreatedAt: progress.CreatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// 新增进展只允许当前执行人操作。进展事实、每个收件人的不可变通知证据和 Outbox
// 在同一事务提交；申请人按 USER 命名空间通知，其他当前执行人按 PERSON 命名空间通知，
// 作者自身不会收到重复提醒。
func (s *Service) AddProgress(ctx context.Context, actor Actor, id uint64, key string, in AddProgressInput) (*ProgressLog, error) {
	if !actor.Can("presale.progress") || actor.PersonID == "" {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return nil, ErrInvalidInput
	}
	content := strings.TrimSpace(in.Content)
	if len([]rune(content)) < 1 || len([]rune(content)) > 2000 {
		return nil, ErrInvalidInput
	}
	if in.ProgressPct != nil && *in.ProgressPct > 100 {
		return nil, ErrInvalidInput
	}
	linkURL := strings.TrimSpace(in.LinkURL)
	if linkURL != "" {
		u, e := url.Parse(linkURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || len([]rune(linkURL)) > 1000 {
			return nil, ErrInvalidInput
		}
	}
	// 进展按纯文本保存，在写入边界拒绝标签字符；即使未来消费者遗漏上下文转义，也不会
	// 把不可变历史记录直接解释成可执行 HTML。
	if strings.ContainsAny(content, "<>") {
		return nil, ErrInvalidInput
	}
	payload, err := json.Marshal(struct {
		RequestID   uint64 `json:"request_id"`
		ActorID     string `json:"actor_id"`
		Content     string `json:"content"`
		LinkURL     string `json:"link_url"`
		ProgressPct *uint8 `json:"progress_pct"`
		Version     uint64 `json:"version"`
	}{RequestID: id, ActorID: actor.UserID, Content: content, LinkURL: linkURL, ProgressPct: in.ProgressPct, Version: in.Version})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	requestHash := hex.EncodeToString(digest[:])
	if old, findErr := s.repo.FindProgressByKey(ctx, actor.TenantID, key); findErr == nil {
		if old.RequestHash != requestHash {
			return nil, ErrIdempotencyConflict
		}
		return old, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	var created *ProgressLog
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		requestValue, findErr := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if findErr != nil {
			return findErr
		}
		// 先锁父申请再查询幂等键，使同一时间线的并发提交串行化，避免可重复读快照中两个事务
		// 同时判断键不存在。
		if old, findErr := s.repo.FindProgressByKeyForUpdate(tx, actor.TenantID, key); findErr == nil {
			if old.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			created = old
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if requestValue.Version != in.Version {
			return ErrVersionConflict
		}
		if requestValue.Status != StatusExecuting {
			return ErrInvalidTransition
		}
		assignments, findErr := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
		if findErr != nil {
			return findErr
		}
		current := false
		for _, assignment := range assignments {
			if assignment.AssigneeID == actor.PersonID {
				current = true
				break
			}
		}
		if !current {
			return ErrForbidden
		}
		created = &ProgressLog{TenantID: actor.TenantID, RequestID: id, AuthorID: actor.UserID, Content: content, LinkURL: linkURL, ProgressPct: in.ProgressPct, IdempotencyKey: key, RequestHash: requestHash, CreatedAt: s.clock.Now()}
		if createErr := s.repo.CreateProgress(tx, created); createErr != nil {
			return createErr
		}
		return s.recordProgressNotifications(tx, actor, requestValue, created, assignments)
	})
	if err != nil {
		// 不同申请行不共享父锁；它们争用同一租户级幂等键时由唯一索引选出胜者，再把已提交
		// 记录转换成稳定的重放或冲突响应，不向调用方暴露 MySQL 重复键错误。
		if old, findErr := s.repo.FindProgressByKey(ctx, actor.TenantID, key); findErr == nil {
			if old.RequestHash != requestHash {
				return nil, ErrIdempotencyConflict
			}
			return old, nil
		}
	}
	return created, err
}

// 撤销权限随状态变化：审批中仅申请人可撤销，审批通过后仅组长或具备专门撤销权限者可操作。
// 申请行锁、版本号和绑定操作者的幂等摘要共同防止并发覆盖和跨资源重放。
func (s *Service) Cancel(ctx context.Context, actor Actor, id uint64, key string, in CancelInput) error {
	key = strings.TrimSpace(key)
	in.Reason = strings.TrimSpace(in.Reason)
	if key == "" || len(key) > 128 || in.Reason == "" || len([]rune(in.Reason)) > 2000 {
		return ErrInvalidInput
	}
	hash, err := mutationDigest(actor, id, "CANCEL", "CANCEL", struct {
		Reason  string `json:"reason"`
		Version uint64 `json:"version"`
	}{in.Reason, in.Version})
	if err != nil {
		return err
	}
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		allowedActor := r.ApplicantID == actor.UserID || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.Can("presale.cancel")
		if !allowedActor {
			return ErrForbidden
		}
		if oldReplay, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			return validateMutationReplay(oldReplay, actor, id, "CANCEL", "CANCEL", hash)
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Version != in.Version {
			return ErrVersionConflict
		}
		allowed := (r.Status == StatusPendingApproval && r.ApplicantID == actor.UserID) || ((r.Status == StatusApprovedPendingAssignment || r.Status == StatusExecuting) && (actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.Can("presale.cancel")))
		if !allowed {
			return ErrForbidden
		}
		now := s.clock.Now()
		from := r.Status
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{"status": StatusCancelled, "cancelled_at": now, "updated_by": actor.UserID}); e != nil {
			return e
		}
		if e = s.statusLog(tx, r, from, StatusCancelled, "CANCELLED", in.Reason, actor.UserID, actor.RequestID); e != nil {
			return e
		}
		return s.repo.CreateMutationReplay(tx, newMutationReplay(actor, id, key, "CANCEL", "CANCEL", hash, r.Version, now))
	})
	if err == nil || errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrNotFound) {
		return err
	}
	if !isDuplicateMutationError(err) {
		return err
	}
	return s.resolveMutationRace(ctx, actor, id, key, "CANCEL", "CANCEL", hash, err, func(value *PresaleRequest) error {
		if value.ApplicantID == actor.UserID || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.Can("presale.cancel") {
			return nil
		}
		return ErrForbidden
	}, nil)
}

// 工时只能由当前执行人写入执行中的申请，并完全保存在 CRM 内部。创建工时和完成条件
// 判断同事务提交；当所有当前执行人至少存在一条未作废工时后，最后一次写入会原子推进为完成态。
func (s *Service) AddWorklog(ctx context.Context, actor Actor, id uint64, key string, in AddWorklogInput) (*Worklog, error) {
	if !actor.Can("presale.worklog") || actor.PersonID == "" {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return nil, ErrInvalidInput
	}
	hours, e := calculateHours(in.RawUnit, in.RawValue, s.dayHours)
	if e != nil || !in.WorkEnd.After(in.WorkStart) || in.WorkStart.After(s.clock.Now().Add(5*time.Minute)) || strings.TrimSpace(in.WorkSiteAddress) == "" || !validContent(in.WorkContent) || len([]rune(in.Remark)) > 1000 {
		return nil, ErrInvalidInput
	}
	requestHash, legacyHash, e := worklogRequestHashes(actor, id, in)
	if e != nil {
		return nil, e
	}
	var out *Worklog
	e = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		assignments, e := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		current := false
		var mine Assignment
		for _, a := range assignments {
			if a.AssigneeID == actor.PersonID {
				current = true
				mine = a
			}
		}
		if !current {
			return ErrForbidden
		}
		// 先核验父资源范围和当前执行关系，再查询租户级幂等键。首次写入即使自动完成任务，
		// 精确重试仍可返回原工时；不同操作者或资源不能借同一键取得旧记录。
		if old, findErr := s.repo.FindWorklogByKey(tx, actor.TenantID, key); findErr == nil {
			if !sameWorklogReplay(old, actor, id, requestHash, legacyHash) {
				return ErrIdempotencyConflict
			}
			out = old
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Version != in.Version {
			return ErrVersionConflict
		}
		if r.Status != StatusExecuting {
			return ErrInvalidTransition
		}
		overlap, e := s.repo.HasOverlappingWorklog(tx, actor.TenantID, id, actor.PersonID, in.WorkStart.UTC(), in.WorkEnd.UTC())
		if e != nil {
			return e
		}
		if overlap {
			return ErrInvalidInput
		}
		now := s.clock.Now()
		no, e := s.repo.NextWorklogNo(tx, actor.TenantID, now)
		if e != nil {
			return e
		}
		w := &Worklog{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, WorklogNo: no, RequestID: id, PersonID: actor.PersonID, DepartmentSnapshot: mine.AssigneeDepartmentSnapshot, PersonNameSnapshot: mine.AssigneeNameSnapshot, WorkStart: in.WorkStart.UTC(), WorkEnd: in.WorkEnd.UTC(), RawUnit: in.RawUnit, RawValue: normalizeDecimal(in.RawValue), ConversionFactor: factorFor(in.RawUnit, s.dayHours), WorkHours: hours, Unit: "HOUR", WorkSiteAddress: strings.TrimSpace(in.WorkSiteAddress), WorkContent: in.WorkContent, Remark: strings.TrimSpace(in.Remark), PushStatus: PushSuccess, IdempotencyKey: key, RequestHash: requestHash}
		if e = s.repo.CreateWorklog(tx, w); e != nil {
			return e
		}
		// 工时只记录投入，不再自动结束申请；结束必须由当前技术人员显式点击“结束”。
		w.CompletedTask = false
		out = w
		return nil
	})
	if e != nil {
		// 租户级键可能跨不同申请父锁竞争；解析唯一键胜者前必须重新完成父申请和执行人授权。
		var replay *Worklog
		parentAuthorized := false
		replayErr := s.repo.WithTransaction(ctx, func(tx context.Context) error {
			if _, findErr := s.repo.FindRequestForUpdate(tx, actor.TenantID, id); findErr != nil {
				return findErr
			}
			assignments, findErr := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
			if findErr != nil {
				return findErr
			}
			if _, current := currentAssignment(assignments, actor.PersonID); !current {
				return ErrForbidden
			}
			parentAuthorized = true
			old, findErr := s.repo.FindWorklogByKey(tx, actor.TenantID, key)
			if errors.Is(findErr, ErrNotFound) {
				return nil
			}
			if findErr != nil {
				return findErr
			}
			if !sameWorklogReplay(old, actor, id, requestHash, legacyHash) {
				return ErrIdempotencyConflict
			}
			replay = old
			return nil
		})
		if replayErr == nil {
			if replay != nil {
				return replay, nil
			}
			return nil, e
		}
		if errors.Is(replayErr, ErrIdempotencyConflict) || errors.Is(replayErr, ErrForbidden) || (errors.Is(replayErr, ErrNotFound) && !parentAuthorized) {
			return nil, replayErr
		}
	}
	return out, e
}

func (s *Service) Complete(ctx context.Context, actor Actor, id uint64, in CompletePresaleInput) (*PresaleRequest, error) {
	if !actor.Can("presale.complete") || actor.PersonID == "" || (!actor.HasRole("technician") && !actor.HasRole("project_manager") && !actor.HasRole("team_lead")) {
		return nil, ErrForbidden
	}
	if in.Version == 0 || len([]rune(in.Reason)) > 1000 {
		return nil, ErrInvalidInput
	}
	var out *PresaleRequest
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		r, e := s.repo.FindRequestForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		if r.Status != StatusExecuting || r.Version != in.Version {
			return ErrInvalidTransition
		}
		assignments, e := s.repo.ListCurrentAssignmentsForUpdate(tx, actor.TenantID, id)
		if e != nil {
			return e
		}
		current, executableAssignment := false, false
		for _, a := range assignments {
			if a.AssigneeID == actor.PersonID {
				current = true
				executableAssignment = a.AssigneeRole == "team_lead" || a.AssigneeRole == "project_manager" || a.AssigneeRole == "technician" || a.AssigneeRole == "implementation_engineer"
				break
			}
		}
		if !current || !executableAssignment {
			return ErrForbidden
		}
		now := s.clock.Now()
		if e = s.repo.UpdateRequestVersioned(tx, r, r.Version, map[string]any{"status": StatusCompleted, "completed_at": now, "updated_by": actor.UserID}); e != nil {
			return e
		}
		if e = s.statusLog(tx, r, StatusExecuting, StatusCompleted, "MANUAL_COMPLETION", strings.TrimSpace(in.Reason), actor.UserID, actor.RequestID); e != nil {
			return e
		}
		if e = s.notifyWorkflow(tx, WorkflowNotification{TenantID: actor.TenantID, RecipientID: r.ApplicantID, Type: "PRESALE_COMPLETED", Title: "售前支持已完成", Body: "技术人员已人工结束售前支持流程", RequestID: r.ID, RequestNo: r.RequestNo}); e != nil {
			return e
		}
		r.Status, r.CompletedAt = StatusCompleted, &now
		out = r
		return nil
	})
	return out, err
}

func worklogRequestHashes(actor Actor, requestID uint64, in AddWorklogInput) (string, string, error) {
	canonical := struct {
		RequestID       uint64    `json:"request_id"`
		ActorID         string    `json:"actor_id"`
		PersonID        string    `json:"person_id"`
		WorkStart       time.Time `json:"work_start"`
		WorkEnd         time.Time `json:"work_end"`
		RawUnit         string    `json:"raw_unit"`
		RawValue        string    `json:"raw_value"`
		WorkSiteAddress string    `json:"work_site_address"`
		WorkContent     string    `json:"work_content"`
		Remark          string    `json:"remark"`
		Version         uint64    `json:"version"`
	}{
		RequestID: requestID, ActorID: actor.UserID, PersonID: actor.PersonID,
		WorkStart: in.WorkStart.UTC(), WorkEnd: in.WorkEnd.UTC(), RawUnit: in.RawUnit,
		RawValue: normalizeDecimal(in.RawValue), WorkSiteAddress: strings.TrimSpace(in.WorkSiteAddress),
		WorkContent: in.WorkContent, Remark: strings.TrimSpace(in.Remark), Version: in.Version,
	}
	hash, err := requestDigest(canonical)
	if err != nil {
		return "", "", err
	}
	legacyHash, err := requestDigest(in)
	return hash, legacyHash, err
}

func sameWorklogReplay(old *Worklog, actor Actor, requestID uint64, hash, legacyHash string) bool {
	if old == nil || old.RequestID != requestID || old.PersonID != actor.PersonID || old.CreatedBy != actor.UserID {
		return false
	}
	return old.RequestHash == hash || old.RequestHash == legacyHash
}

func currentAssignment(assignments []Assignment, personID string) (Assignment, bool) {
	for _, assignment := range assignments {
		if assignment.AssigneeID == personID {
			return assignment, true
		}
	}
	return Assignment{}, false
}

func requestDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func mutationDigest(actor Actor, requestID uint64, operation, action string, payload any) (string, error) {
	return requestDigest(struct {
		ActorID   string `json:"actor_id"`
		RequestID uint64 `json:"request_id"`
		Operation string `json:"operation"`
		Action    string `json:"action"`
		Payload   any    `json:"payload"`
	}{actor.UserID, requestID, operation, action, payload})
}

func newMutationReplay(actor Actor, requestID uint64, key, operation, action, hash string, responseVersion uint64, at time.Time) *MutationReplay {
	return &MutationReplay{
		TenantID: actor.TenantID, RequestID: requestID, Operation: operation, Action: action,
		ActorID: actor.UserID, IdempotencyKey: key, RequestHash: hash,
		ResponseVersion: responseVersion, RequestIDTrace: actor.RequestID, CreatedAt: at,
	}
}

func validateMutationReplay(value *MutationReplay, actor Actor, requestID uint64, operation, action, hash string) error {
	if value == nil || value.RequestID != requestID || value.ActorID != actor.UserID || value.Operation != operation || value.Action != action || value.RequestHash != hash {
		return ErrIdempotencyConflict
	}
	return nil
}

func approvalReplayAction(node uint8, action string) string {
	return fmt.Sprintf("NODE_%d_%s", node, action)
}

func validateApprovalReplay(value *MutationReplay, actor Actor, requestID uint64, action, hash string, instance *ApprovalInstance) error {
	if value == nil || value.RequestID != requestID || value.ActorID != actor.UserID || value.Operation != "APPROVAL_ACTION" || value.RequestHash != hash {
		return ErrIdempotencyConflict
	}
	if value.Action != approvalReplayAction(1, action) && value.Action != approvalReplayAction(2, action) {
		return ErrIdempotencyConflict
	}
	return approvalReplayRoleAllowed(actor, value.Action, instance)
}

// approvalReplayRoleAllowed 复用 approvalNodeRoleAllowedForInstance 的动态节点角色判断：
// 有规则快照时按快照的 role_code 校验，历史无快照实例回退到旧的角色映射。
func approvalReplayRoleAllowed(actor Actor, replayAction string, instance *ApprovalInstance) error {
	var node uint8
	switch {
	case strings.HasPrefix(replayAction, "NODE_1_"):
		node = 1
	case strings.HasPrefix(replayAction, "NODE_2_"):
		node = 2
	default:
		return ErrForbidden
	}
	if approvalNodeRoleAllowedForInstance(actor, node, instance) {
		return nil
	}
	return ErrForbidden
}

func approvalNodeRoleAllowed(actor Actor, node uint8) bool {
	if actor.HasRole("crm_super_admin") {
		return node == 1 || node == 2
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

// assignmentActionAllowed is the single runtime gate for post-approval
// assignment actions. The approval rule is snapshotted on the instance, so a
// rule change only affects newly-created requests and cannot alter an active
// workflow halfway through.
func assignmentActionAllowed(actor Actor, instance *ApprovalInstance, action ApprovalNodeType) bool {
	if actor.HasRole("crm_super_admin") {
		return true
	}
	// Preserve the legacy behavior for requests created before rule snapshots
	// were available.
	if instance == nil || len(instance.NodesJSON) == 0 {
		return (action == ApprovalNodeDepartment && actor.HasRole("technical_director")) ||
			(action == ApprovalNodePerson && (actor.HasRole("technical_director") || actor.HasRole("team_lead")))
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil {
		return false
	}
	for roleCode := range actor.Roles {
		if configured, ok := AssignmentActionForRole(nodes, roleCode); ok && configured == action {
			return true
		}
	}
	return false
}

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

func isDuplicateMutationError(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &mysqlErr) && mysqlErr.Number == 1062)
}

func (s *Service) resolveApprovalMutationRace(ctx context.Context, actor Actor, requestID uint64, key, action, hash string) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.repo.FindRequestForUpdate(tx, actor.TenantID, requestID); err != nil {
			return err
		}
		instance, err := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, requestID)
		if err != nil {
			return err
		}
		old, err := s.repo.FindMutationReplay(tx, actor.TenantID, requestID, actor.UserID, key)
		if errors.Is(err, ErrNotFound) {
			return ErrIdempotencyConflict
		}
		if err != nil {
			return err
		}
		return validateApprovalReplay(old, actor, requestID, action, hash, instance)
	})
}

// 这里仅处理首个事务失败后的存储层唯一键竞争：先锁定并授权请求中的父申请，再查询
// 租户级键，避免猜测键值泄露胜者资源；普通业务冲突在进入该恢复路径前已经返回。
func (s *Service) resolveMutationRace(
	ctx context.Context,
	actor Actor,
	requestID uint64,
	key, operation, action, hash string,
	original error,
	authorize func(*PresaleRequest) error,
	setResponse func(*PresaleRequest),
) error {
	var replayed bool
	replayErr := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		requestValue, err := s.repo.FindRequestForUpdate(tx, actor.TenantID, requestID)
		if err != nil {
			return err
		}
		if authorize != nil {
			if err = authorize(requestValue); err != nil {
				return err
			}
		}
		old, err := s.repo.FindMutationReplay(tx, actor.TenantID, requestID, actor.UserID, key)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = validateMutationReplay(old, actor, requestID, operation, action, hash); err != nil {
			return err
		}
		replayed = true
		if setResponse != nil {
			setResponse(requestValue)
		}
		return nil
	})
	if replayErr != nil {
		return replayErr
	}
	if replayed {
		return nil
	}
	if isDuplicateMutationError(original) {
		return ErrIdempotencyConflict
	}
	return original
}

func (s *Service) completeIfReady(ctx context.Context, r *PresaleRequest, current []Assignment, operator, trace string) (bool, error) {
	if r.Status != StatusExecuting || len(current) == 0 {
		return false, nil
	}
	ids := make([]string, 0, len(current))
	for _, a := range current {
		ids = append(ids, a.AssigneeID)
	}
	have, e := s.repo.AssigneeIDsWithValidWorklogs(ctx, r.TenantID, r.ID, ids)
	if e != nil {
		return false, e
	}
	for _, id := range ids {
		if !have[id] {
			return false, nil
		}
	}
	now := s.clock.Now()
	from := r.Status
	if e = s.repo.UpdateRequestVersioned(ctx, r, r.Version, map[string]any{"status": StatusCompleted, "completed_at": now, "updated_by": operator}); e != nil {
		return false, e
	}
	if e = s.statusLog(ctx, r, from, StatusCompleted, completionPolicy, "", operator, trace); e != nil {
		return false, e
	}
	return true, nil
}

func calculateHours(unit, value, dayHours string) (string, error) {
	if !decimalPattern.MatchString(value) {
		return "", ErrInvalidInput
	}
	v, ok := new(big.Rat).SetString(value)
	if !ok || v.Sign() <= 0 {
		return "", ErrInvalidInput
	}
	factor := big.NewRat(1, 1)
	switch unit {
	case "小时", "HOUR":
	case "人天", "PERSON_DAY":
		var ok bool
		factor, ok = new(big.Rat).SetString(dayHours)
		if !ok {
			return "", ErrInvalidInput
		}
	default:
		return "", ErrInvalidInput
	}
	v.Mul(v, factor)
	return v.FloatString(2), nil
}
func normalizeDecimal(v string) string { r, _ := new(big.Rat).SetString(v); return r.FloatString(2) }
func factorFor(unit, dayHours string) string {
	if unit == "人天" || unit == "PERSON_DAY" {
		return dayHours
	}
	return "1.00"
}
func validContent(v string) bool {
	switch v {
	case "方案设计", "技术交流", "POC演示", "技术答疑", "其他", "SOLUTION_DESIGN", "TECH_EXCHANGE", "POC_DEMO", "TECH_QA", "OTHER":
		return true
	}
	return false
}
func (s *Service) statusLog(ctx context.Context, r *PresaleRequest, from, to RequestStatus, trigger, reason, operator, trace string) error {
	return s.repo.CreateStatusLog(ctx, &StatusLog{TenantID: r.TenantID, RequestID: r.ID, FromStatus: from, ToStatus: to, Trigger: trigger, Reason: reason, OperatorID: operator, OccurredAt: s.clock.Now(), RequestIDTrace: trace})
}
func (s *Service) event(tenant, typ, aggregate, id string, payload any) (*OutboxEvent, error) {
	b, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	return &OutboxEvent{EventID: s.ids.NewID(), TenantID: tenant, EventType: typ, AggregateType: aggregate, AggregateID: id, Payload: b, Status: "PENDING", CreatedAt: s.clock.Now()}, nil
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
func (s *Service) requireReadable(ctx context.Context, actor Actor, value *PresaleRequest) error {
	if actor.HasRole("sales_director") || actor.HasRole("technical_director") || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.HasRole("auditor") {
		return nil
	}
	if actor.HasRole("sales") && value.ApplicantID == actor.UserID {
		return nil
	}
	if actor.PersonID == "" {
		return ErrForbidden
	}
	assignments, err := s.repo.ListAssignments(ctx, actor.TenantID, value.ID)
	if err != nil {
		return err
	}
	for _, assignment := range assignments {
		if assignment.AssigneeID == actor.PersonID {
			return nil
		}
	}
	return ErrForbidden
}
