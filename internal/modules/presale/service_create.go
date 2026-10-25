package presale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
		// 售前申请不会驱动商机阶段，但只能在未终态的推进阶段创建。该检查在
		// 同一事务内由商机端加锁，避免商机并发签约、失败或作废后仍插入新申请。
		if e := s.opportunities.EnsurePresaleEligible(tx, actor, opp.ID); e != nil {
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
		roundStartedAt := approvalRoundStart(now)
		inst := &ApprovalInstance{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestID: r.ID, EngineInstanceID: instanceID, Status: "PENDING", CurrentNode: 1, StartedAt: &roundStartedAt}
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

// ReopenRequest 复用现有请求和审批实例 ID。只有终态的
// 驳回或取消请求可重新打开，其历史保持完整。

// ReopenRequest 复用现有请求和审批实例 ID。只有终态的
// 驳回或取消请求可重新打开，其历史保持完整。
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
		// 重新发起会回到待审批状态，等同于一次新的售前资源协调请求；商机必须仍在
		// 可推进阶段。该调用在当前事务内锁定商机，不会自动调整其销售阶段。
		if err = s.opportunities.EnsurePresaleEligible(tx, actor, r.OpportunityID); err != nil {
			return err
		}
		previousStatus := r.Status
		inst, err := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, id)
		if err != nil {
			return err
		}
		now := approvalRoundStart(s.clock.Now())
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
		// 重开轮次以新 started_at 作为通知去重键：历史已读通知不会被复用，
		// 同一轮内的投递重试仍保持稳定。
		inst.StartedAt = &now
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
	if len([]rune(strings.TrimSpace(in.ServiceAddress))) > 500 || len([]rune(strings.TrimSpace(in.ContactName))) > 128 ||
		len([]rune(strings.TrimSpace(in.ContactPhone))) > 64 || len([]rune(strings.TrimSpace(in.Description))) > 60000 {
		return ErrInvalidInput
	}
	return nil
}

// 只有审批引擎确认实例创建后才推进本地状态。事件序号用于吞掉重复或乱序回执，
// 请求行和审批实例行同时加锁以缩小两个状态投影的竞争窗口。已进入待审批状态时当前实现
// 按幂等成功返回，并不会再次核对重复事件的引擎实例 ID 或事件身份。
