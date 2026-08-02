package customer

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
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

const customerMergedEventType = "CUSTOMER_MERGED"

// MergeRepository is separate from the regular customer repository because a
// merge coordinates several already-existing CRM aggregates. Keeping this
// boundary explicit also prevents a caller from treating a partial table
// update as a completed merge.
type MergeRepository interface {
	WithMergeTransaction(context.Context, func(context.Context) error) error
	LockCustomersForMerge(context.Context, auth.Principal, uint64, uint64) (*Customer, *Customer, error)
	LockMergeRelations(context.Context, string, uint64) error
	FindMergeIdempotency(context.Context, string, string, string) (*MergeIdempotency, error)
	MergeBlockers(context.Context, string, uint64, uint64) ([]MergeBlocker, error)
	MigrateMergeRelations(context.Context, string, uint64, uint64, string, time.Time) (MergeMigrationCounts, error)
	MarkCustomersMerged(context.Context, *Customer, *Customer, uint64, uint64, string, time.Time) error
	CreateMergeLog(context.Context, *MergeLog) error
	CreateMergeIdempotency(context.Context, *MergeIdempotency) error
	CreateMergeOutbox(context.Context, *MergeOutboxEvent) error
	CreateChangeLog(context.Context, *ChangeLog) error
}

// Merge makes the target customer the surviving master. The method requires
// both records to remain in the caller's data scope while their rows are locked.
func (s *Service) Merge(ctx context.Context, input MergeRequest) (*MergeResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.merge == nil {
		return nil, ErrMergeUnavailable
	}
	if input.SourceCustomerID == input.TargetCustomerID {
		return nil, ErrMergeSameCustomer
	}
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.SourceCustomerID == 0 || input.TargetCustomerID == 0 || input.SourceVersion == 0 || input.TargetVersion == 0 || input.Reason == "" {
		return nil, apperror.New(422, "CRM_CUSTOMER_MERGE_INVALID_ARGUMENT", "source, target, versions and reason are required")
	}
	if input.IdempotencyKey == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(input.IdempotencyKey) > 128 {
		return nil, apperror.New(422, "COMMON_IDEMPOTENCY_KEY_INVALID", "Idempotency-Key must not exceed 128 characters")
	}
	requestHash := mergeRequestHash(input)
	var result *MergeResponse
	err = s.merge.WithMergeTransaction(ctx, func(txCtx context.Context) error {
		source, target, lockErr := s.merge.LockCustomersForMerge(txCtx, principal, input.SourceCustomerID, input.TargetCustomerID)
		if lockErr != nil {
			return lockErr
		}
		prior, findErr := s.merge.FindMergeIdempotency(txCtx, principal.TenantID, principal.UserID, input.IdempotencyKey)
		if findErr != nil {
			return findErr
		}
		if prior != nil {
			if prior.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			var replay MergeResponse
			if decodeErr := json.Unmarshal(prior.ResponseJSON, &replay); decodeErr != nil {
				return decodeErr
			}
			result = &replay
			return nil
		}
		if source.Status != StatusActive || target.Status != StatusActive || source.MergedIntoID != nil || target.MergedIntoID != nil {
			return ErrMergeInactive
		}
		if source.Version != input.SourceVersion || target.Version != input.TargetVersion {
			return ErrVersionConflict
		}
		if relationLockErr := s.merge.LockMergeRelations(txCtx, principal.TenantID, source.ID); relationLockErr != nil {
			return relationLockErr
		}
		blockers, blockerErr := s.merge.MergeBlockers(txCtx, principal.TenantID, source.ID, target.ID)
		if blockerErr != nil {
			return blockerErr
		}
		if len(blockers) != 0 {
			return apperror.WithDetails(ErrMergeBlocked, map[string]any{"blockers": blockers})
		}
		now := s.now().UTC()
		counts, migrateErr := s.merge.MigrateMergeRelations(txCtx, principal.TenantID, source.ID, target.ID, principal.UserID, now)
		if migrateErr != nil {
			return migrateErr
		}
		if markErr := s.merge.MarkCustomersMerged(txCtx, source, target, input.SourceVersion, input.TargetVersion, principal.UserID, now); markErr != nil {
			return markErr
		}
		result = &MergeResponse{
			SourceCustomerID: source.ID, TargetCustomerID: target.ID, SourceStatus: StatusMerged,
			MergedIntoID: target.ID, SourceVersion: input.SourceVersion + 1,
			TargetVersion: input.TargetVersion + 1, MigratedCounts: counts, CompletedAt: now,
		}
		encodedCounts, encodeErr := json.Marshal(counts)
		if encodeErr != nil {
			return encodeErr
		}
		log := &MergeLog{TenantID: principal.TenantID, SourceCustomerID: source.ID, TargetCustomerID: target.ID, SourceVersion: input.SourceVersion, TargetVersion: input.TargetVersion, MigratedCountsJSON: encodedCounts, Reason: input.Reason, OperatorID: principal.UserID, RequestID: requestctx.ID(txCtx), OccurredAt: now}
		if logErr := s.merge.CreateMergeLog(txCtx, log); logErr != nil {
			return logErr
		}
		if logErr := s.writeMergeChangeLogs(txCtx, principal, input, now); logErr != nil {
			return logErr
		}
		encodedResponse, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		if idemErr := s.merge.CreateMergeIdempotency(txCtx, &MergeIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: input.IdempotencyKey, RequestHash: requestHash, ResponseJSON: encodedResponse, CreatedAt: now}); idemErr != nil {
			return idemErr
		}
		payload, encodeErr := json.Marshal(map[string]any{"source_customer_id": source.ID, "target_customer_id": target.ID, "migrated_counts": counts})
		if encodeErr != nil {
			return encodeErr
		}
		outbox := &MergeOutboxEvent{EventID: mergeEventID(principal.TenantID, principal.UserID, input.IdempotencyKey), TenantID: principal.TenantID, EventType: customerMergedEventType, AggregateType: "customer", AggregateID: stringUint(target.ID), Payload: payload, Status: "PENDING", CreatedAt: now}
		if outboxErr := s.merge.CreateMergeOutbox(txCtx, outbox); outboxErr != nil {
			return outboxErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "MERGE", ResourceType: "customer", ResourceID: stringUint(source.ID), ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, BeforeJSON: audit.JSON(map[string]any{"source_status": StatusActive, "source_version": input.SourceVersion, "target_version": input.TargetVersion}), AfterJSON: audit.JSON(result), Reason: input.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		if replay, ok := s.replayMerge(ctx, principal, input, requestHash); ok {
			return replay, nil
		}
		return nil, err
	}
	return result, nil
}

func (s *Service) replayMerge(ctx context.Context, principal auth.Principal, input MergeRequest, requestHash string) (*MergeResponse, bool) {
	var result *MergeResponse
	err := s.merge.WithMergeTransaction(ctx, func(txCtx context.Context) error {
		if _, _, lockErr := s.merge.LockCustomersForMerge(txCtx, principal, input.SourceCustomerID, input.TargetCustomerID); lockErr != nil {
			return lockErr
		}
		prior, findErr := s.merge.FindMergeIdempotency(txCtx, principal.TenantID, principal.UserID, input.IdempotencyKey)
		if findErr != nil || prior == nil || prior.RequestHash != requestHash {
			return ErrIdempotencyConflict
		}
		var replay MergeResponse
		if decodeErr := json.Unmarshal(prior.ResponseJSON, &replay); decodeErr != nil {
			return decodeErr
		}
		result = &replay
		return nil
	})
	return result, err == nil && result != nil
}

func (s *Service) writeMergeChangeLogs(ctx context.Context, principal auth.Principal, input MergeRequest, now time.Time) error {
	requestID := requestctx.ID(ctx)
	logs := []ChangeLog{
		{TenantID: principal.TenantID, CustomerID: input.SourceCustomerID, FieldName: "status", BeforeJSON: audit.JSON(StatusActive), AfterJSON: audit.JSON(StatusMerged), Reason: input.Reason, OperatorID: principal.UserID, RequestID: requestID, OccurredAt: now},
		{TenantID: principal.TenantID, CustomerID: input.SourceCustomerID, FieldName: "merged_into_id", BeforeJSON: audit.JSON(nil), AfterJSON: audit.JSON(input.TargetCustomerID), Reason: input.Reason, OperatorID: principal.UserID, RequestID: requestID, OccurredAt: now},
		{TenantID: principal.TenantID, CustomerID: input.TargetCustomerID, FieldName: "merged_source_customer_id", BeforeJSON: audit.JSON(nil), AfterJSON: audit.JSON(input.SourceCustomerID), Reason: input.Reason, OperatorID: principal.UserID, RequestID: requestID, OccurredAt: now},
	}
	for index := range logs {
		if err := s.merge.CreateChangeLog(ctx, &logs[index]); err != nil {
			return err
		}
	}
	return nil
}

func mergeRequestHash(input MergeRequest) string {
	value := stringUint(input.SourceCustomerID) + "\x00" + stringUint(input.TargetCustomerID) + "\x00" + stringUint(input.SourceVersion) + "\x00" + stringUint(input.TargetVersion) + "\x00" + input.Reason
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mergeEventID(tenantID, actorID, key string) string {
	sum := sha256.Sum256([]byte("customer-merge\x00" + tenantID + "\x00" + actorID + "\x00" + key))
	return hex.EncodeToString(sum[:])
}
