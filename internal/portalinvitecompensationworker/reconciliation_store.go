package portalinvitecompensationworker

import (
	"context"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	"gorm.io/gorm"
)

type reconciliationStoreGORM struct{ db *gorm.DB }

func newReconciliationStore(db *gorm.DB) *reconciliationStoreGORM {
	return &reconciliationStoreGORM{db: db}
}

func (s *reconciliationStoreGORM) startRun(ctx context.Context, runID, workerID string, now time.Time) error {
	return s.db.WithContext(ctx).Exec(`INSERT INTO crm_portal_identity_reconciliation_runs
 (run_id,worker_id,status,started_at) VALUES (?,?, 'RUNNING',?)`, runID, workerID, now).Error
}

const reconciliationCandidatesSQL = `SELECT
 l.id AS link_id,l.tenant_id,l.customer_id,l.contact_id,l.platform_user_id,l.portal_account_id,l.status AS crm_status,
 COALESCE((SELECT i.account_no FROM crm_portal_invites i
   WHERE i.tenant_id=l.tenant_id AND i.customer_id=l.customer_id AND i.contact_id=l.contact_id
     AND i.platform_user_id=l.platform_user_id AND i.deleted_at IS NULL
   ORDER BY i.id DESC LIMIT 1),'') AS account_no,
 (c.id IS NOT NULL) AS customer_exists,COALESCE(c.status,'') AS customer_status,(c.merged_into_id IS NOT NULL) AS customer_merged,
 (ct.id IS NOT NULL) AS contact_valid,
 COALESCE((SELECT task.status FROM crm_portal_compensation_tasks task
   WHERE task.tenant_id=l.tenant_id AND task.customer_id=l.customer_id AND task.contact_id=l.contact_id
     AND task.platform_user_id=l.platform_user_id AND task.deleted_at IS NULL
   ORDER BY task.id DESC LIMIT 1),'') AS compensation_status
FROM crm_portal_identity_links l
LEFT JOIN crm_customers c ON c.tenant_id=l.tenant_id AND c.id=l.customer_id AND c.deleted_at IS NULL
LEFT JOIN crm_customer_contacts ct ON ct.tenant_id=l.tenant_id AND ct.id=l.contact_id AND ct.customer_id=l.customer_id
 AND ct.is_registration=TRUE AND ct.deleted_at IS NULL
WHERE l.deleted_at IS NULL AND l.id>?
ORDER BY l.id ASC LIMIT ?`

func (s *reconciliationStoreGORM) listCandidates(ctx context.Context, after uint64, limit int) ([]reconciliationCandidate, error) {
	values := make([]reconciliationCandidate, 0, limit)
	err := s.db.WithContext(ctx).Raw(reconciliationCandidatesSQL, after, limit).Scan(&values).Error
	return values, err
}

func (s *reconciliationStoreGORM) persistObservation(ctx context.Context, runID string, now time.Time, candidate reconciliationCandidate, portal *portalinvite.PortalIdentitySnapshot, finding *reconciliationFinding) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if finding == nil {
			return tx.Exec(`UPDATE crm_portal_identity_reconciliation_findings
 SET status='RESOLVED',resolved_at=?,updated_at=?,last_run_id=?
 WHERE tenant_id=? AND crm_identity_link_id=? AND status='OPEN'`, now, now, runID, candidate.TenantID, candidate.LinkID).Error
		}
		key := reconciliationFindingKey(candidate, finding.Code)
		if err := tx.Exec(`UPDATE crm_portal_identity_reconciliation_findings
 SET status='RESOLVED',resolved_at=?,updated_at=?,last_run_id=?
 WHERE tenant_id=? AND crm_identity_link_id=? AND finding_key<>? AND status='OPEN'`, now, now, runID, candidate.TenantID, candidate.LinkID, key).Error; err != nil {
			return err
		}
		portalStatus, portalAccountID := "", candidate.PortalAccountID
		if portal != nil && portal.Found {
			portalStatus, portalAccountID = portal.Status, portal.PortalAccountID
		}
		return tx.Exec(`INSERT INTO crm_portal_identity_reconciliation_findings
 (finding_key,tenant_id,crm_identity_link_id,finding_code,resolution_mode,status,crm_status,portal_status,
  customer_id,contact_id,platform_user_id,portal_account_id,compensation_status,first_detected_at,last_detected_at,
  occurrence_count,resolved_at,last_run_id,created_at,updated_at)
 VALUES (?,?,?,?,?,'OPEN',?,?,?,?,?,?,?,?,?,1,NULL,?,?,?)
 ON DUPLICATE KEY UPDATE resolution_mode=VALUES(resolution_mode),status='OPEN',crm_status=VALUES(crm_status),
  portal_status=VALUES(portal_status),portal_account_id=VALUES(portal_account_id),compensation_status=VALUES(compensation_status),
  last_detected_at=VALUES(last_detected_at),occurrence_count=occurrence_count+1,resolved_at=NULL,
  last_run_id=VALUES(last_run_id),updated_at=VALUES(updated_at)`,
			key, candidate.TenantID, candidate.LinkID, finding.Code, finding.ResolutionMode,
			candidate.CRMStatus, portalStatus, candidate.CustomerID, candidate.ContactID, candidate.PlatformUserID,
			portalAccountID, candidate.CompensationStatus, now, now, runID, now, now).Error
	})
}

func (s *reconciliationStoreGORM) finishRun(ctx context.Context, runID string, now time.Time, metrics reconciliationMetrics, errorCode string) error {
	status := "SUCCEEDED"
	if errorCode != "" {
		status = "FAILED"
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE crm_portal_identity_reconciliation_runs
 SET status=?,scanned_count=?,consistent_count=?,auto_compensation_count=?,needs_review_count=?,error_code=?,completed_at=?
 WHERE run_id=? AND status='RUNNING'`, status, metrics.Scanned, metrics.Consistent, metrics.AutoCompensation, metrics.NeedsReview, errorCode, now, runID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}
