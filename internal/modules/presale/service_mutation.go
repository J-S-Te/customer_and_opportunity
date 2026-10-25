package presale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"
)

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
		allowed := (r.Status == StatusPendingApproval && r.ApplicantID == actor.UserID) || ((r.Status == StatusApprovedPendingAssignment || r.Status == StatusPendingExecutionAssignment || r.Status == StatusExecuting) && (actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.Can("presale.cancel")))
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
