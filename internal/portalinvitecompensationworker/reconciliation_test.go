package portalinvitecompensationworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalinvite"
)

func TestClassifyPlatformBinding(t *testing.T) {
	tests := []struct {
		name      string
		crmStatus string
		found     bool
		status    string
		wantCode  string
		wantNil   bool
	}{
		{name: "active and bound", crmStatus: "ACTIVE", found: true, status: "ACTIVE", wantNil: true},
		{name: "active but missing", crmStatus: "ACTIVE", found: false, wantCode: "PLATFORM_BINDING_MISSING"},
		{name: "active but disabled", crmStatus: "ACTIVE", found: true, status: "DISABLED", wantCode: "PLATFORM_BINDING_STATUS_MISMATCH"},
		{name: "disabled and unbound", crmStatus: "DISABLED", found: false, wantNil: true},
		{name: "disabled and bound disabled", crmStatus: "DISABLED", found: true, status: "DISABLED", wantNil: true},
		{name: "disabled but bound active", crmStatus: "DISABLED", found: true, status: "ACTIVE", wantCode: "PLATFORM_BINDING_STATUS_MISMATCH"},
		{name: "pending invite any binding", crmStatus: "PENDING", found: true, status: "ACTIVE", wantNil: true},
		{name: "unknown crm status", crmStatus: "BROKEN", found: true, status: "ACTIVE", wantCode: "CRM_IDENTITY_STATUS_UNKNOWN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finding := classifyPlatformBinding(reconciliationCandidate{CRMStatus: test.crmStatus}, test.status, test.found)
			if test.wantNil {
				if finding != nil {
					t.Fatalf("finding = %#v, want nil", finding)
				}
				return
			}
			if finding == nil || finding.Code != test.wantCode {
				t.Fatalf("finding = %#v, want code %s", finding, test.wantCode)
			}
		})
	}
}

func TestReconciliationCandidateQueryBindsCustomerContactAndRegistrationContact(t *testing.T) {
	for _, fragment := range []string{
		"c.tenant_id=l.tenant_id", "c.deleted_at IS NULL", "ct.tenant_id=l.tenant_id",
		"ct.customer_id=l.customer_id", "ct.is_registration=TRUE", "ct.deleted_at IS NULL",
	} {
		if !strings.Contains(reconciliationCandidatesSQL, fragment) {
			t.Fatalf("candidate query is missing fail-closed binding %q", fragment)
		}
	}
}

type reconciliationObservation struct {
	candidate reconciliationCandidate
	finding   *reconciliationFinding
}

type fakeReconciliationStore struct {
	pages        [][]reconciliationCandidate
	page         int
	started      int
	finished     int
	finishCode   string
	finishMetric reconciliationMetrics
	observations []reconciliationObservation
}

func (s *fakeReconciliationStore) startRun(context.Context, string, string, time.Time) error {
	s.started++
	return nil
}
func (s *fakeReconciliationStore) listCandidates(context.Context, uint64, int) ([]reconciliationCandidate, error) {
	if s.page >= len(s.pages) {
		return nil, nil
	}
	value := s.pages[s.page]
	s.page++
	return value, nil
}
func (s *fakeReconciliationStore) persistObservation(_ context.Context, _ string, _ time.Time, candidate reconciliationCandidate, _ *portalinvite.PortalIdentitySnapshot, finding *reconciliationFinding) error {
	s.observations = append(s.observations, reconciliationObservation{candidate: candidate, finding: finding})
	return nil
}
func (s *fakeReconciliationStore) finishRun(_ context.Context, _ string, _ time.Time, metrics reconciliationMetrics, code string) error {
	s.finished++
	s.finishMetric, s.finishCode = metrics, code
	return nil
}

type fakeReconciliationPortal struct {
	snapshots map[string]portalinvite.PortalIdentitySnapshot
	err       error
}

func (p fakeReconciliationPortal) ReconciliationSnapshots(_ context.Context, subjects []string) ([]portalinvite.PortalIdentitySnapshot, error) {
	if p.err != nil {
		return nil, p.err
	}
	result := make([]portalinvite.PortalIdentitySnapshot, 0, len(subjects))
	for _, subject := range subjects {
		result = append(result, p.snapshots[subject])
	}
	return result, nil
}

func reconciliationCandidateFixture(id uint64, subject string) reconciliationCandidate {
	return reconciliationCandidate{
		LinkID: id, TenantID: "tenant-a", CustomerID: 7, ContactID: 9,
		PlatformUserID: subject, PortalAccountID: "PA42", CRMStatus: "ACTIVE",
		AccountNo: "customer-a", CustomerExists: true, CustomerStatus: "ACTIVE", ContactValid: true,
	}
}

func reconciliationSnapshotFixture(subject string) portalinvite.PortalIdentitySnapshot {
	contactID := uint64(9)
	return portalinvite.PortalIdentitySnapshot{
		PlatformUserID: subject, Found: true, PortalAccountID: "PA42", AccountNo: "customer-a",
		CustomerID: 7, ContactID: &contactID, Status: "ACTIVE", Version: 3,
	}
}

func TestReconcilerRecordsConsistentAutoCompensationAndNeedsReview(t *testing.T) {
	consistent := reconciliationCandidateFixture(1, "subject-1")
	pending := reconciliationCandidateFixture(2, "subject-2")
	pending.CompensationStatus = "RETRY_WAIT"
	deadLetter := reconciliationCandidateFixture(3, "subject-3")
	deadLetter.CompensationStatus = "DEAD_LETTER"
	store := &fakeReconciliationStore{pages: [][]reconciliationCandidate{{consistent, pending}, {deadLetter}}}
	portal := fakeReconciliationPortal{snapshots: map[string]portalinvite.PortalIdentitySnapshot{
		"subject-1": reconciliationSnapshotFixture("subject-1"),
		"subject-2": {PlatformUserID: "subject-2", Found: false},
		"subject-3": {PlatformUserID: "subject-3", Found: false},
	}}
	reconciler := newReconciler(store, portal, "worker-a", 2)
	reconciler.newRunID = func() string { return "run-a" }
	metrics, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metrics != (reconciliationMetrics{Scanned: 3, Consistent: 1, AutoCompensation: 1, NeedsReview: 1}) {
		t.Fatalf("metrics=%#v", metrics)
	}
	if store.started != 1 || store.finished != 1 || store.finishCode != "" || len(store.observations) != 3 {
		t.Fatalf("store=%#v", store)
	}
	if got := store.observations[1].finding; got == nil || got.ResolutionMode != resolutionAutoCompensation {
		t.Fatalf("pending compensation finding=%#v", got)
	}
	if got := store.observations[2].finding; got == nil || got.Code != "PORTAL_LINK_MISSING_COMPENSATION_DEAD_LETTER" || got.ResolutionMode != resolutionNeedsReview {
		t.Fatalf("dead letter finding=%#v", got)
	}
}

func TestReconcilerFailsRunWithoutGuessingAfterPortalFailure(t *testing.T) {
	store := &fakeReconciliationStore{pages: [][]reconciliationCandidate{{reconciliationCandidateFixture(1, "subject-1")}}}
	reconciler := newReconciler(store, fakeReconciliationPortal{err: errors.New("portal unavailable")}, "worker-a", 100)
	reconciler.newRunID = func() string { return "run-a" }
	metrics, err := reconciler.RunOnce(context.Background())
	if err == nil || metrics.Scanned != 0 || len(store.observations) != 0 {
		t.Fatalf("metrics=%#v error=%v observations=%#v", metrics, err, store.observations)
	}
	if store.finished != 1 || store.finishCode != "RECONCILIATION_FAILED" {
		t.Fatalf("failed run was not durably closed: %#v", store)
	}
}

func TestStatusAndMappingDifferencesAlwaysNeedReview(t *testing.T) {
	candidate := reconciliationCandidateFixture(1, "subject-1")
	snapshot := reconciliationSnapshotFixture("subject-1")
	snapshot.Status = "DISABLED"
	if got := classifyReconciliation(candidate, snapshot); got == nil || got.Code != "IDENTITY_STATUS_MISMATCH" || got.ResolutionMode != resolutionNeedsReview {
		t.Fatalf("status mismatch=%#v", got)
	}
	snapshot = reconciliationSnapshotFixture("subject-1")
	snapshot.CustomerID = 8
	if got := classifyReconciliation(candidate, snapshot); got == nil || got.Code != "IDENTITY_MAPPING_MISMATCH" || got.ResolutionMode != resolutionNeedsReview {
		t.Fatalf("mapping mismatch=%#v", got)
	}
}
