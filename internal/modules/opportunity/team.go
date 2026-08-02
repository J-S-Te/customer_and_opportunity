package opportunity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

const ownerNotificationEventType = "OPPORTUNITY_OWNER_CHANGED_NOTIFICATION"

var memberRoles = map[string]struct{}{
	MemberRoleSalesSupport:     {},
	MemberRoleTechnicalSupport: {},
	MemberRoleBusinessSupport:  {},
	MemberRoleOther:            {},
}

// ChangeOwner replaces the single owner under optimistic locking. Target IDs
// are platform OIDC subjects, and the injected platform owner directory
// validates the selected subject/organization membership before persistence.
func (s *Service) ChangeOwner(ctx context.Context, id uint64, input ChangeOwnerRequest) (*Response, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ownerID, ownerOrgID, reason := strings.TrimSpace(input.OwnerUserID), strings.TrimSpace(input.OwnerOrgID), strings.TrimSpace(input.Reason)
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, ErrIdempotencyRequired
	}
	if ownerID == "" {
		return nil, ErrOwnerRequired
	}
	if reason == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CHANGE_REASON_REQUIRED", "change reason is required")
	}
	if s.owners != nil {
		if err = s.owners.Validate(ctx, ownerID, ownerOrgID); err != nil {
			return nil, err
		}
	}
	requestHash := ownerRequestHash(ownerID, ownerOrgID, input.Version, reason)
	prior, err := s.repo.FindChangeIdempotency(ctx, principal.TenantID, id, "OWNER_CHANGE", principal.UserID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if prior.RequestHash != requestHash {
			return nil, ErrIdempotencyConflict
		}
		var replay Response
		if err := json.Unmarshal(prior.ResponseJSON, &replay); err != nil {
			return nil, err
		}
		return &replay, nil
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if model.OwnerUserID == ownerID && model.OwnerOrgID == ownerOrgID {
		// A semantic no-op does not consume a version or generate audit/outbox
		// noise. The response is still persisted for stable key replay.
		result := toResponse(model)
		encodedResponse, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err := s.repo.CreateChangeIdempotency(ctx, &ChangeIdempotency{TenantID: principal.TenantID, OpportunityID: id, Operation: "OWNER_CHANGE", ActorID: principal.UserID, Key: idempotencyKey, RequestHash: requestHash, ResponseJSON: encodedResponse, CreatedAt: s.now()}); err != nil {
			return s.replayOwnerChange(ctx, principal, id, idempotencyKey, requestHash, err)
		}
		return &result, nil
	}
	before := toResponse(model)
	oldOwnerID := model.OwnerUserID
	model.OwnerUserID, model.OwnerOrgID, model.UpdatedBy = ownerID, ownerOrgID, principal.UserID
	newVersion := input.Version + 1
	events, err := ownerNotificationEvents(principal.TenantID, model, oldOwnerID, ownerID, newVersion, s.now())
	if err != nil {
		return nil, err
	}
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if updateErr := s.repo.UpdateOwner(txCtx, model, input.Version); updateErr != nil {
			return updateErr
		}
		// Alert recipients are versioned by owner. A transfer must cancel the
		// old owner's unread alert immediately; the next scan creates the new
		// owner's alert under the same threshold version if still overdue.
		if cancelErr := cancelActiveStageAlerts(txCtx, s.db, principal.TenantID, model.ID, principal.UserID, s.now()); cancelErr != nil {
			return cancelErr
		}
		if outboxErr := s.repo.CreateOutboxEvents(txCtx, events); outboxErr != nil {
			return outboxErr
		}
		encodedResponse, encodeErr := json.Marshal(toResponse(model))
		if encodeErr != nil {
			return encodeErr
		}
		if idemErr := s.repo.CreateChangeIdempotency(txCtx, &ChangeIdempotency{TenantID: principal.TenantID, OpportunityID: id, Operation: "OWNER_CHANGE", ActorID: principal.UserID, Key: idempotencyKey, RequestHash: requestHash, ResponseJSON: encodedResponse, CreatedAt: s.now()}); idemErr != nil {
			return idemErr
		}
		return s.audit.Write(txCtx, audit.Event{
			TenantID: principal.TenantID, Module: "opportunity", Operation: "OWNER_CHANGE",
			ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID,
			ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before),
			AfterJSON: audit.JSON(toResponse(model)), Reason: reason, Result: "SUCCESS",
		})
	})
	if err != nil {
		// A same-key concurrent request can lose the version race after the
		// winning transaction commits. Re-read the actor-bound durable result
		// before surfacing the underlying version/unique-key error.
		return s.replayOwnerChange(ctx, principal, id, idempotencyKey, requestHash, err)
	}
	result := toResponse(model)
	return &result, nil
}

func (s *Service) replayOwnerChange(ctx context.Context, principal auth.Principal, id uint64, key, requestHash string, original error) (*Response, error) {
	prior, findErr := s.repo.FindChangeIdempotency(ctx, principal.TenantID, id, "OWNER_CHANGE", principal.UserID, key)
	if findErr != nil || prior == nil {
		return nil, original
	}
	if prior.RequestHash != requestHash {
		return nil, ErrIdempotencyConflict
	}
	var replay Response
	if err := json.Unmarshal(prior.ResponseJSON, &replay); err != nil {
		return nil, err
	}
	return &replay, nil
}

// GetMembers returns current rows by default. includeInactive also returns
// deactivated subjects, but it is not a complete sequence of repeated
// membership terms; the immutable business audit is authoritative for changes.
func (s *Service) GetMembers(ctx context.Context, id uint64, includeInactive bool) (*TeamResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	members, err := s.repo.ListMembers(ctx, principal.TenantID, id, includeInactive)
	if err != nil {
		return nil, err
	}
	return &TeamResponse{OpportunityID: id, Version: model.Version, Members: memberResponses(members)}, nil
}

// ListMemberTerms returns independently queryable membership intervals after
// proving access to the parent opportunity. A LEGACY_SNAPSHOT row carries a
// snapshot_at observation and active_at_snapshot state, never a fabricated
// pre-migration start or end time.
func (s *Service) ListMemberTerms(ctx context.Context, id uint64, query MemberTermQuery) (pagination.Page[MemberTermResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return pagination.Page[MemberTermResponse]{}, err
	}
	query.UserID = strings.TrimSpace(query.UserID)
	if len(query.UserID) > 64 || validatePagination(query.Page, query.PageSize) != nil {
		return pagination.Page[MemberTermResponse]{}, ErrInvalidQuery
	}
	if _, err = s.repo.FindByID(ctx, principal, id); err != nil {
		return pagination.Page[MemberTermResponse]{}, err
	}
	return s.repo.ListMemberTerms(ctx, principal.TenantID, id, query)
}

// ReplaceMembers atomically replaces the current set, reactivates historical
// rows when necessary, and deactivates removed rows without physical deletion.
func (s *Service) ReplaceMembers(ctx context.Context, id uint64, input ReplaceMembersRequest) (*TeamResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return nil, apperror.New(422, "CRM_OPPORTUNITY_CHANGE_REASON_REQUIRED", "change reason is required")
	}
	desired, err := normalizeMembers(input.Members, principal, id, s.now())
	if err != nil {
		return nil, err
	}
	model, err := s.repo.FindByID(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if model.Status == StatusVoid {
		return nil, ErrInactive
	}
	current, err := s.repo.ListMembers(ctx, principal.TenantID, id, false)
	if err != nil {
		return nil, err
	}
	if sameMemberSet(current, desired) && model.Version == input.Version {
		return &TeamResponse{OpportunityID: id, Version: model.Version, Members: memberResponses(current)}, nil
	}
	if model.Version != input.Version {
		return nil, ErrVersionConflict
	}
	before := memberResponses(current)
	now := s.now()
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if replaceErr := s.repo.ReplaceMembers(txCtx, model, input.Version, desired, now); replaceErr != nil {
			return replaceErr
		}
		return s.audit.Write(txCtx, audit.Event{
			TenantID: principal.TenantID, Module: "opportunity", Operation: "TEAM_REPLACE",
			ResourceType: "opportunity", ResourceID: uintString(id), ActorID: principal.UserID,
			ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(before),
			AfterJSON: audit.JSON(memberResponses(desired)), Reason: reason, Result: "SUCCESS",
		})
	})
	if err != nil {
		return nil, err
	}
	current, err = s.repo.ListMembers(ctx, principal.TenantID, id, false)
	if err != nil {
		return nil, err
	}
	return &TeamResponse{OpportunityID: id, Version: model.Version, Members: memberResponses(current)}, nil
}

func normalizeMembers(input []TeamMemberInput, principal auth.Principal, opportunityID uint64, now time.Time) ([]Member, error) {
	if len(input) > 50 {
		return nil, ErrTeamTooLarge
	}
	result := make([]Member, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		userID, role := strings.TrimSpace(item.UserID), strings.ToUpper(strings.TrimSpace(item.Role))
		if userID == "" {
			return nil, ErrOwnerRequired
		}
		if _, valid := memberRoles[role]; !valid {
			return nil, ErrInvalidMemberRole
		}
		if _, duplicate := seen[userID]; duplicate {
			return nil, ErrDuplicateMember
		}
		seen[userID] = struct{}{}
		model := Member{OpportunityID: opportunityID, UserID: userID, Role: role, IsActive: true}
		model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, principal.UserID, principal.UserID, 1
		model.CreatedAt, model.UpdatedAt = now, now
		result = append(result, model)
	}
	return result, nil
}

func sameMemberSet(current, desired []Member) bool {
	if len(current) != len(desired) {
		return false
	}
	roles := make(map[string]string, len(current))
	for _, member := range current {
		roles[member.UserID] = member.Role
	}
	for _, member := range desired {
		if roles[member.UserID] != member.Role {
			return false
		}
	}
	return true
}

func memberResponses(models []Member) []MemberResponse {
	result := make([]MemberResponse, 0, len(models))
	for _, model := range models {
		result = append(result, MemberResponse{ID: model.ID, UserID: model.UserID, Role: model.Role, IsActive: model.IsActive, EndedAt: model.EndedAt, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt})
	}
	return result
}

func ownerNotificationEvents(tenantID string, opportunity *Opportunity, oldOwnerID, newOwnerID string, version uint64, now time.Time) ([]OutboxEvent, error) {
	// An organization reassignment with the same human owner is auditable but is
	// not a person-to-person handover, so it must not create owner notifications.
	if oldOwnerID == newOwnerID {
		return nil, nil
	}
	targets := []struct {
		userID string
		kind   string
	}{{oldOwnerID, "PREVIOUS_OWNER"}, {newOwnerID, "NEW_OWNER"}}
	events := make([]OutboxEvent, 0, len(targets))
	for _, target := range targets {
		payload, err := json.Marshal(map[string]any{
			"opportunity_id": opportunity.ID, "opportunity_no": opportunity.OpportunityNo,
			"opportunity_name": opportunity.Name, "recipient_user_id": target.userID,
			"recipient_kind": target.kind, "owner_user_id": newOwnerID,
			"target_path": "/customer-opportunity/opportunities?opportunity_id=" + uintString(opportunity.ID), "version": version,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, OutboxEvent{
			EventID: ownerEventID(tenantID, opportunity.ID, version, target.kind), TenantID: tenantID,
			EventType: ownerNotificationEventType, AggregateType: "opportunity", AggregateID: uintString(opportunity.ID),
			Payload: payload, Status: "PENDING", CreatedAt: now,
		})
	}
	return events, nil
}

func ownerEventID(tenantID string, opportunityID, version uint64, kind string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + uintString(opportunityID) + "\x00" + uintString(version) + "\x00" + kind))
	return hex.EncodeToString(sum[:])
}

func ownerRequestHash(ownerID, ownerOrgID string, version uint64, reason string) string {
	sum := sha256.Sum256([]byte(ownerID + "\x00" + ownerOrgID + "\x00" + uintString(version) + "\x00" + reason))
	return hex.EncodeToString(sum[:])
}
