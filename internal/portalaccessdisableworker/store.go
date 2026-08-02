package portalaccessdisableworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
)

var errLeaseLost = errors.New("Portal access disable lease was lost")

type store struct{ db *gorm.DB }

func newStore(db *gorm.DB) *store { return &store{db: db} }

const claimOperationsSQL = `SELECT * FROM crm_portal_access_disable_operations
WHERE deleted_at IS NULL AND stage IN ('PREPARED','MAPPING_DISABLED') AND
 ((status='RETRY_WAIT' AND next_retry_at<=?)
  OR (status='PROCESSING' AND locked_until<?)
  OR (status='PROCESSING' AND locked_until IS NULL AND updated_at<=?))
ORDER BY created_at,id LIMIT ? FOR UPDATE SKIP LOCKED`

func (s *store) claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]portalinvite.AccessDisableOperation, error) {
	var claimed []portalinvite.AccessDisableOperation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operations []portalinvite.AccessDisableOperation
		if err := tx.Raw(claimOperationsSQL, now, now, now.Add(-lease), limit).Scan(&operations).Error; err != nil {
			return err
		}
		if len(operations) == 0 {
			return nil
		}
		ids := make([]uint64, len(operations))
		for i := range operations {
			ids[i] = operations[i].ID
		}
		until := now.Add(lease)
		result := tx.Model(&portalinvite.AccessDisableOperation{}).Where("id IN ?", ids).Updates(map[string]any{
			"status": portalinvite.DisableStatusProcessing, "locked_by": workerID, "locked_until": until,
			"updated_by": workerID, "updated_at": now, "version": gorm.Expr("version+1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errLeaseLost
		}
		for i := range operations {
			operations[i].Status, operations[i].LockedBy, operations[i].LockedUntil = portalinvite.DisableStatusProcessing, workerID, &until
			operations[i].UpdatedAt, operations[i].UpdatedBy, operations[i].Version = now, workerID, operations[i].Version+1
		}
		claimed = operations
		return nil
	})
	return claimed, err
}

func (s *store) failInvalid(ctx context.Context, operation portalinvite.AccessDisableOperation, workerID string, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt := operation.Attempts + 1
		status := portalinvite.DisableStatusRetryWait
		next := any(now.Add(disableBackoff(attempt)))
		if attempt >= 8 {
			status, next = portalinvite.DisableStatusDeadLetter, nil
		}
		result := tx.Model(&portalinvite.AccessDisableOperation{}).
			Where("id=? AND tenant_id=? AND version=? AND status=? AND locked_by=? AND locked_until>?", operation.ID, operation.TenantID, operation.Version, portalinvite.DisableStatusProcessing, workerID, now).
			Updates(map[string]any{
				"status": status, "attempts": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil,
				"last_error_code": "INVALID_RECOVERY_OPERATION", "last_error_summary": "Portal access disable recovery operation is invalid",
				"updated_by": workerID, "updated_at": now, "version": gorm.Expr("version+1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		if status != portalinvite.DisableStatusDeadLetter {
			return nil
		}
		auditCtx := database.WithHandle(requestctx.WithID(ctx, "portal-disable-worker:"+operation.OperationNo), tx)
		return audit.NewGORMWriter(tx).Write(auditCtx, audit.Event{
			TenantID: operation.TenantID, Module: "portal_invite", Operation: "DISABLE_ACCESS_RECOVERY",
			ResourceType: "customer", ResourceID: fmt.Sprint(operation.CustomerID), ActorID: operation.ActorID,
			Reason: "Recovery operation validation failed", AfterJSON: audit.JSON(map[string]any{"status": status, "stage": operation.Stage, "operation_no": operation.OperationNo, "error_code": "INVALID_RECOVERY_OPERATION"}), Result: "FAILED",
		})
	})
}

func disableBackoff(attempt uint16) time.Duration {
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<attempt) * time.Minute
}
