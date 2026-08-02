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
}

func (s *Service) UseApprovalTaskResolver(resolver ApprovalTaskResolver) *Service {
	s.approvalTasks = resolver
	return s
}

// UseWorkerReadiness installs persisted liveness evidence for the independent
// delivery worker. New requests fail closed when no instance has a fresh
// heartbeat; already committed idempotent replays remain readable.
func (s *Service) UseWorkerReadiness(readiness WorkerReadiness, maxAge time.Duration) *Service {
	s.workerReadiness = readiness
	s.workerMaxAge = maxAge
	return s
}

// UseAuditWriter installs the mandatory privacy-audit sink used before any
// decrypted contact phone is released. ContactPhone fails closed when this
// dependency is unavailable.
func (s *Service) UseAuditWriter(writer audit.Writer) *Service {
	s.auditWriter = writer
	return s
}

func NewService(repo Repository, opportunities OpportunityReader, phones PhoneProtector, clock Clock, ids IDGenerator) *Service {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Service{repo: repo, opportunities: opportunities, phones: phones, clock: clock, ids: ids, dayHours: "8.00"}
}

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
	hash, legacyHash, err := createRequestHashes(actor, in)
	if err != nil {
		return nil, err
	}
	// Authorize the requested opportunity before looking up a tenant-wide key.
	// Otherwise a guessed key could disclose an application created by another
	// salesperson or for an opportunity outside the caller's data scope.
	if old, findErr := s.repo.FindRequestByCreateKey(ctx, actor.TenantID, key); findErr == nil {
		if !sameCreateRequestReplay(old, actor, opp.ID, hash, legacyHash) {
			return nil, ErrIdempotencyConflict
		}
		return old, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}
	if !s.deliveryWorkerReady(ctx) {
		return nil, ErrDependencyUnavailable
	}
	cipher, err := s.phones.Encrypt(ctx, in.ContactPhone)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
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
		// Recheck inside the write transaction so a stale result from a long
		// request cannot admit work after every worker heartbeat has expired.
		if !s.deliveryWorkerReady(tx) {
			return ErrDependencyUnavailable
		}
		no, e := s.repo.NextRequestNo(tx, actor.TenantID, now)
		if e != nil {
			return e
		}
		r := &PresaleRequest{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestNo: no, OpportunityID: opp.ID, OpportunityNoSnapshot: opp.OpportunityNo, ApplicantID: actor.UserID, ApplicantNameSnapshot: actor.UserName, Venue: in.Venue, ServiceAddress: strings.TrimSpace(in.ServiceAddress), ContactName: strings.TrimSpace(in.ContactName), ContactPhoneCipher: cipher, ContactPhoneMasked: s.phones.Mask(in.ContactPhone), Description: strings.TrimSpace(in.Description), ExpectedStart: in.ExpectedStart.UTC(), ExpectedEnd: in.ExpectedEnd.UTC(), Urgency: in.Urgency, Status: StatusApprovalStarting, CreateIdempotencyKey: key, CreateRequestHash: hash}
		if e = s.repo.CreateRequest(tx, r); e != nil {
			return e
		}
		inst := &ApprovalInstance{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestID: r.ID, Status: "STARTING"}
		if e = s.repo.CreateApprovalInstance(tx, inst); e != nil {
			return e
		}
		event, e := s.event(actor.TenantID, "PRESALE_APPROVAL_START_REQUESTED", "presale_request", fmt.Sprint(r.ID), map[string]any{"request_id": r.ID, "request_no": r.RequestNo, "applicant_id": r.ApplicantID, "opportunity_id": r.OpportunityID})
		if e != nil {
			return e
		}
		if e = s.repo.CreateOutbox(tx, event); e != nil {
			return e
		}
		created = r
		return nil
	})
	if err != nil {
		// Requests for different opportunities do not share a parent-row lock.
		// Resolve a concurrently committed tenant-wide key to the same bound
		// replay/conflict semantics instead of exposing a database duplicate-key
		// error or returning another actor's resource.
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
	// The legacy digest is accepted only together with exact persisted actor and
	// parent bindings. This keeps safe retries across a rolling deployment while
	// preventing the historical payload-only hash from authorizing a replay.
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
	n := len([]rune(strings.TrimSpace(in.Description)))
	if n < 10 || n > 2000 || in.ExpectedEnd.Before(in.ExpectedStart) {
		return ErrInvalidInput
	}
	return nil
}

// MarkApprovalStarted moves the local request only after the real approval
// engine has acknowledged instance creation.
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
		return s.statusLog(tx, r, StatusApprovalStarting, StatusPendingApproval, "APPROVAL_STARTED", "", "approval-engine", "")
	})
}

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
	if !actor.HasRole("sales_director") && !actor.HasRole("team_lead") {
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
		currentNodeAllowed := approvalNodeRoleAllowed(actor, r.CurrentApprovalNode)
		if old, findErr := s.repo.FindMutationReplay(tx, actor.TenantID, id, actor.UserID, key); findErr == nil {
			return validateApprovalReplay(old, actor, id, in.Action, hash)
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if r.Status != StatusPendingApproval || r.Version != in.Version {
			return ErrInvalidTransition
		}
		if !currentNodeAllowed {
			return ErrForbidden
		}
		if s.approvalTasks == nil {
			return ErrDependencyUnavailable
		}
		instance, instanceErr := s.repo.FindApprovalInstanceForUpdate(tx, actor.TenantID, id)
		if instanceErr != nil || instance.EngineInstanceID == "" || instance.CurrentNode != r.CurrentApprovalNode || instance.Status != "PENDING" {
			return ErrDependencyUnavailable
		}
		// Exactly one action may be in flight for an authoritative task. A second
		// key or opposite action must not overwrite the callback binding while the
		// first outbox event is being delivered.
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

func (s *Service) HandleApprovalCallback(ctx context.Context, tenant string, in ApprovalCallbackInput) error {
	in.EngineInstanceID = strings.TrimSpace(in.EngineInstanceID)
	in.EngineTaskID = strings.TrimSpace(in.EngineTaskID)
	in.ApproverID = strings.TrimSpace(in.ApproverID)
	in.ApproverName = strings.TrimSpace(in.ApproverName)
	in.Result = strings.ToUpper(strings.TrimSpace(in.Result))
	in.Comment = strings.TrimSpace(in.Comment)
	if in.RequestID == 0 || in.EventSequence == 0 || in.OccurredAt.IsZero() || in.Node < 1 || in.Node > 2 ||
		!validApprovalTaskIdentity(in.EngineInstanceID) || !validApprovalTaskIdentity(in.EngineTaskID) || !validApprovalTaskIdentity(in.ApproverID) ||
		in.ApproverName == "" || len([]rune(in.ApproverName)) > 128 || len([]rune(in.Comment)) > 2000 ||
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
		} else if in.Node == 1 {
			fields["current_approval_node"] = 2
			instFields["current_node"] = 2
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
		} else if in.Node == 2 {
			to = StatusApprovedPendingAssignment
		}
		if to != from {
			return s.statusLog(tx, r, from, to, "APPROVAL_CALLBACK", in.Comment, in.ApproverID, log.RequestIDTrace)
		}
		return nil
	})
}

func (s *Service) ReplaceAssignments(ctx context.Context, actor Actor, id uint64, key string, in ReplaceAssignmentsInput) (*PresaleRequest, error) {
	if !actor.Can("presale.assign") || !actor.HasRole("team_lead") {
		return nil, ErrForbidden
	}
	key = strings.TrimSpace(key)
	in.ChangeReason = strings.TrimSpace(in.ChangeReason)
	if key == "" || len(key) > 128 || len(in.Assignees) == 0 || len(in.Assignees) > 100 || in.ChangeReason == "" || len([]rune(in.ChangeReason)) > 1000 {
		return nil, ErrInvalidInput
	}
	targets := map[string]AssignmentTarget{}
	allowedRoles := map[string]bool{"technical_director": true, "team_lead": true, "project_manager": true, "implementation_engineer": true}
	for _, v := range in.Assignees {
		if strings.TrimSpace(v.PersonID) == "" || !allowedRoles[v.Role] {
			return nil, ErrInvalidInput
		}
		v.PersonID = strings.TrimSpace(v.PersonID)
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
		engineers, e := s.repo.FindEngineersForUpdate(tx, actor.TenantID, ids)
		if e != nil {
			return e
		}
		byID := map[string]Engineer{}
		for _, engineer := range engineers {
			byID[engineer.PersonID] = engineer
		}
		if len(byID) != len(ids) {
			return ErrInvalidInput
		}
		// A PMS deactivation must not erase an active assignment. It can be
		// retained or explicitly removed, but only a currently valid engineer
		// with the authoritative PMS role may be newly assigned.
		for personID, target := range targets {
			if _, retained := current[personID]; retained {
				continue
			}
			engineer := byID[personID]
			if !engineer.ValidFlag || engineer.Role != target.Role {
				return ErrInvalidInput
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
			p := byID[pid]
			a := &Assignment{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, RequestID: id, AssigneeID: pid, AssigneeNameSnapshot: p.PersonName, AssigneeDepartmentSnapshot: p.Department, AssigneeRole: t.Role, AssignedBy: actor.UserID, AssignedAt: now, IsCurrent: true, BatchNo: batch, ChangeReason: in.ChangeReason}
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

// AssignmentNotificationEventID is shared with the projection worker so the
// producer and consumer cannot drift on the immutable event identity contract.
func AssignmentNotificationEventID(tenantID string, requestID, assignmentID uint64, eventType string) string {
	value := tenantID + "\x00" + fmt.Sprint(requestID) + "\x00" + fmt.Sprint(assignmentID) + "\x00" + eventType
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// ProgressNotificationEventID binds a personal inbox projection to the
// immutable progress row and one explicitly namespaced recipient.
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
	// The applicant is a CRM user recipient. Only equality in the same USER
	// namespace can establish that the author should be excluded.
	if request.ApplicantID != "" && request.ApplicantID != actor.UserID {
		recipients = append(recipients, recipient{id: request.ApplicantID, namespace: ProgressRecipientUser, kind: ProgressRecipientApplicant})
	}
	for _, assignment := range assignments {
		// Assignees are PMS person recipients. Never compare or substitute their
		// identifiers with CRM user/sub identifiers.
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
	// Progress is plain text. Reject markup at the write boundary so future
	// consumers cannot accidentally turn an immutable historical record into
	// executable HTML even if they omit contextual output escaping.
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
		// Lock the parent before the key lookup. This serializes concurrent
		// submissions for one immutable timeline and avoids a stale RR snapshot
		// in which two transactions both miss the same idempotency key.
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
		// Different request rows do not share the parent lock. If two such
		// transactions race on one tenant idempotency key, the unique index picks
		// a winner. Resolve the committed record to a stable replay/conflict
		// response instead of exposing a raw MySQL duplicate-key error.
		if old, findErr := s.repo.FindProgressByKey(ctx, actor.TenantID, key); findErr == nil {
			if old.RequestHash != requestHash {
				return nil, ErrIdempotencyConflict
			}
			return old, nil
		}
	}
	return created, err
}

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
		allowedActor := r.ApplicantID == actor.UserID || actor.HasRole("team_lead") || actor.Can("presale.cancel")
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
		allowed := (r.Status == StatusPendingApproval && r.ApplicantID == actor.UserID) || ((r.Status == StatusApprovedPendingAssignment || r.Status == StatusExecuting) && (actor.HasRole("team_lead") || actor.Can("presale.cancel")))
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
		if value.ApplicantID == actor.UserID || actor.HasRole("team_lead") || actor.Can("presale.cancel") {
			return nil
		}
		return ErrForbidden
	}, nil)
}

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
		// Parent scope and the current execution relationship are checked before
		// the tenant-wide key. An exact retry remains valid after its first write
		// automatically completed the task, but a different actor/resource never
		// receives the old worklog.
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
		w := &Worklog{BaseModel: BaseModel{TenantID: actor.TenantID, CreatedBy: actor.UserID, UpdatedBy: actor.UserID, Version: 1}, WorklogNo: no, RequestID: id, PersonID: actor.PersonID, DepartmentSnapshot: mine.AssigneeDepartmentSnapshot, PersonNameSnapshot: mine.AssigneeNameSnapshot, WorkStart: in.WorkStart.UTC(), WorkEnd: in.WorkEnd.UTC(), RawUnit: in.RawUnit, RawValue: normalizeDecimal(in.RawValue), ConversionFactor: factorFor(in.RawUnit, s.dayHours), WorkHours: hours, Unit: "HOUR", WorkSiteAddress: strings.TrimSpace(in.WorkSiteAddress), WorkContent: in.WorkContent, Remark: strings.TrimSpace(in.Remark), PushStatus: PushPending, IdempotencyKey: key, RequestHash: requestHash}
		if e = s.repo.CreateWorklog(tx, w); e != nil {
			return e
		}
		completed, e := s.completeIfReady(tx, r, assignments, actor.UserID, actor.RequestID)
		if e != nil {
			return e
		}
		w.CompletedTask = completed
		if completed {
			if e = s.repo.UpdateWorklogDelivery(tx, actor.TenantID, w.ID, map[string]any{"completed_task": true}); e != nil {
				return e
			}
		}
		payload := map[string]any{"eventType": "PRESALE_WORKLOG_CREATED", "eventVersion": 1, "worklogId": w.WorklogNo, "taskId": r.RequestNo, "opportunityId": r.OpportunityNoSnapshot, "personId": w.PersonID, "personName": w.PersonNameSnapshot, "workStartTime": w.WorkStart, "workEndTime": w.WorkEnd, "unit": "小时", "workHours": w.WorkHours, "rawUnit": w.RawUnit, "rawValue": w.RawValue, "conversionFactor": w.ConversionFactor, "workSiteAddress": w.WorkSiteAddress, "venue": r.Venue, "workContent": w.WorkContent, "idempotencyKey": w.WorklogNo, "occurredAt": now}
		event, e := s.event(actor.TenantID, "PRESALE_WORKLOG_CREATED", "presale_worklog", fmt.Sprint(w.ID), payload)
		if e != nil {
			return e
		}
		if e = s.repo.CreateOutbox(tx, event); e != nil {
			return e
		}
		out = w
		return nil
	})
	if e != nil {
		// A tenant-wide key can race across different request parent locks. Repeat
		// the complete parent/assignee authorization before resolving the winner.
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

func validateApprovalReplay(value *MutationReplay, actor Actor, requestID uint64, action, hash string) error {
	if value == nil || value.RequestID != requestID || value.ActorID != actor.UserID || value.Operation != "APPROVAL_ACTION" || value.RequestHash != hash {
		return ErrIdempotencyConflict
	}
	if value.Action != approvalReplayAction(1, action) && value.Action != approvalReplayAction(2, action) {
		return ErrIdempotencyConflict
	}
	return approvalReplayRoleAllowed(actor, value.Action)
}

func approvalReplayRoleAllowed(actor Actor, replayAction string) error {
	switch {
	case strings.HasPrefix(replayAction, "NODE_1_") && actor.HasRole("sales_director"):
		return nil
	case strings.HasPrefix(replayAction, "NODE_2_") && actor.HasRole("team_lead"):
		return nil
	default:
		return ErrForbidden
	}
}

func approvalNodeRoleAllowed(actor Actor, node uint8) bool {
	return node == 1 && actor.HasRole("sales_director") || node == 2 && actor.HasRole("team_lead")
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
		old, err := s.repo.FindMutationReplay(tx, actor.TenantID, requestID, actor.UserID, key)
		if errors.Is(err, ErrNotFound) {
			return ErrIdempotencyConflict
		}
		if err != nil {
			return err
		}
		return validateApprovalReplay(old, actor, requestID, action, hash)
	})
}

// resolveMutationRace handles only the storage-level unique-key race after the
// first transaction has failed. It locks and authorizes the requested parent
// before consulting the tenant-wide key, so a guessed key cannot disclose the
// winner's resource. Business conflicts are returned before reaching here.
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

// requireReadable enforces the same scope as TS-007 list queries. Managers may
// read all tenant tasks. Sales may read their own applications. Any principal
// with an authoritative PMS person ID may read its current or historical
// assignments; PMS assignment roles are not inferred from CRM OIDC roles.
// This prevents a guessed ID from broadening access beyond a real relation.
func (s *Service) requireReadable(ctx context.Context, actor Actor, value *PresaleRequest) error {
	if actor.HasRole("sales_director") || actor.HasRole("team_lead") || actor.HasRole("technical_lead") || actor.HasRole("auditor") {
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
