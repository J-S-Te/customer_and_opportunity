package portalinvitecompensationworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

const (
	resolutionAutoCompensation = "AUTO_COMPENSATION"
	resolutionNeedsReview      = "NEEDS_REVIEW"
)

type reconciliationCandidate struct {
	LinkID             uint64
	TenantID           string
	CustomerID         uint64
	ContactID          uint64
	PlatformUserID     string
	PortalAccountID    string
	CRMStatus          string
	AccountNo          string
	CustomerExists     bool
	CustomerStatus     string
	CustomerMerged     bool
	ContactValid       bool
	CompensationStatus string
}

type reconciliationFinding struct {
	Code, ResolutionMode string
}

type reconciliationMetrics struct {
	Scanned, Consistent, AutoCompensation, NeedsReview uint64
}

type reconciliationStore interface {
	startRun(context.Context, string, string, time.Time) error
	listCandidates(context.Context, uint64, int) ([]reconciliationCandidate, error)
	persistObservation(context.Context, string, time.Time, reconciliationCandidate, *portalinvite.PortalIdentitySnapshot, *reconciliationFinding) error
	finishRun(context.Context, string, time.Time, reconciliationMetrics, string) error
}

type reconciliationPortal interface {
	ReconciliationSnapshots(context.Context, []string) ([]portalinvite.PortalIdentitySnapshot, error)
}

type Reconciler struct {
	store    reconciliationStore
	portal   reconciliationPortal
	workerID string
	batch    int
	now      func() time.Time
	newRunID func() string
}

func newReconciler(store reconciliationStore, portal reconciliationPortal, workerID string, batch int) *Reconciler {
	return &Reconciler{
		store: store, portal: portal, workerID: workerID, batch: batch,
		now: func() time.Time { return time.Now().UTC() }, newRunID: requestctx.NewID,
	}
}

func (r *Reconciler) RunOnce(ctx context.Context) (metrics reconciliationMetrics, err error) {
	if r == nil || r.store == nil || r.portal == nil || strings.TrimSpace(r.workerID) == "" || r.batch < 1 || r.batch > 100 {
		return metrics, errors.New("Portal identity reconciliation dependencies are incomplete")
	}
	startedAt := r.now().UTC()
	runID := r.newRunID()
	if strings.TrimSpace(runID) == "" || len(runID) > 64 {
		return metrics, errors.New("Portal identity reconciliation run identity is invalid")
	}
	if err = r.store.startRun(ctx, runID, r.workerID, startedAt); err != nil {
		return metrics, err
	}
	defer func() {
		code := ""
		if err != nil {
			code = "RECONCILIATION_FAILED"
		}
		if finishErr := r.store.finishRun(context.WithoutCancel(ctx), runID, r.now().UTC(), metrics, code); finishErr != nil {
			err = errors.Join(err, finishErr)
		}
	}()

	var cursor uint64
	for {
		candidates, listErr := r.store.listCandidates(ctx, cursor, r.batch)
		if listErr != nil {
			return metrics, listErr
		}
		if len(candidates) == 0 {
			return metrics, nil
		}
		subjects := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.LinkID <= cursor || strings.TrimSpace(candidate.PlatformUserID) == "" {
				return metrics, errors.New("Portal identity reconciliation candidate is invalid")
			}
			cursor = candidate.LinkID
			subjects = append(subjects, candidate.PlatformUserID)
		}
		snapshots, snapshotErr := r.portal.ReconciliationSnapshots(ctx, subjects)
		if snapshotErr != nil {
			return metrics, snapshotErr
		}
		bySubject := make(map[string]portalinvite.PortalIdentitySnapshot, len(snapshots))
		for _, snapshot := range snapshots {
			if _, duplicate := bySubject[snapshot.PlatformUserID]; duplicate {
				return metrics, errors.New("Portal identity reconciliation snapshot is duplicated")
			}
			bySubject[snapshot.PlatformUserID] = snapshot
		}
		for _, candidate := range candidates {
			snapshot, present := bySubject[candidate.PlatformUserID]
			if !present {
				return metrics, errors.New("Portal identity reconciliation snapshot is incomplete")
			}
			finding := classifyReconciliation(candidate, snapshot)
			metrics.Scanned++
			if finding == nil {
				metrics.Consistent++
			} else if finding.ResolutionMode == resolutionAutoCompensation {
				metrics.AutoCompensation++
			} else {
				metrics.NeedsReview++
			}
			if persistErr := r.store.persistObservation(ctx, runID, r.now().UTC(), candidate, &snapshot, finding); persistErr != nil {
				return metrics, persistErr
			}
		}
		if len(candidates) < r.batch {
			return metrics, nil
		}
	}
}

func classifyReconciliation(candidate reconciliationCandidate, portal portalinvite.PortalIdentitySnapshot) *reconciliationFinding {
	if !candidate.CustomerExists || candidate.CustomerStatus != "ACTIVE" || candidate.CustomerMerged {
		return needsReview("CRM_CUSTOMER_INACTIVE")
	}
	if !candidate.ContactValid {
		return needsReview("CRM_CONTACT_INVALID")
	}
	if strings.TrimSpace(candidate.AccountNo) == "" {
		return needsReview("CRM_ACCOUNT_NO_MISSING")
	}
	if !portal.Found {
		switch candidate.CompensationStatus {
		case "PENDING", "PROCESSING", "RETRY_WAIT":
			// The existing compensation worker owns execution, leasing and
			// idempotency. Reconciliation only records that automatic repair is
			// already in progress; it never creates a second remote operation.
			return &reconciliationFinding{Code: "PORTAL_LINK_MISSING_COMPENSATION_PENDING", ResolutionMode: resolutionAutoCompensation}
		case "DEAD_LETTER":
			return needsReview("PORTAL_LINK_MISSING_COMPENSATION_DEAD_LETTER")
		default:
			return needsReview("PORTAL_LINK_MISSING")
		}
	}
	if portal.PlatformUserID != candidate.PlatformUserID || portal.CustomerID != candidate.CustomerID ||
		portal.ContactID == nil || *portal.ContactID != candidate.ContactID || portal.PortalAccountID != candidate.PortalAccountID ||
		portal.AccountNo != candidate.AccountNo {
		return needsReview("IDENTITY_MAPPING_MISMATCH")
	}
	if candidate.CRMStatus != portal.Status {
		// DISABLED/ACTIVE/PENDING transitions are policy-bearing and may revoke
		// sessions or access. Never infer a direction from timestamps.
		return needsReview("IDENTITY_STATUS_MISMATCH")
	}
	if candidate.CRMStatus != "PENDING" && candidate.CRMStatus != "ACTIVE" && candidate.CRMStatus != "DISABLED" {
		return needsReview("CRM_IDENTITY_STATUS_UNKNOWN")
	}
	return nil
}

func needsReview(code string) *reconciliationFinding {
	return &reconciliationFinding{Code: code, ResolutionMode: resolutionNeedsReview}
}

func reconciliationFindingKey(candidate reconciliationCandidate, code string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", candidate.TenantID, candidate.LinkID, code)))
	return hex.EncodeToString(digest[:])
}
