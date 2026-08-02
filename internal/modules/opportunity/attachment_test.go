package opportunity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type attachmentRepoStub struct {
	values    map[string]*Attachment
	writes    int
	createErr error
}

func (r *attachmentRepoStub) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *attachmentRepoStub) Create(_ context.Context, value *Attachment) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.writes++
	copy := *value
	copy.ID = uint64(len(r.values) + 1)
	value.ID = copy.ID
	r.values[value.PublicID] = &copy
	return nil
}
func (r *attachmentRepoStub) FindCreate(_ context.Context, tenant, actor, key string) (*Attachment, error) {
	for _, value := range r.values {
		if value.TenantID == tenant && value.CreateActorID == actor && value.CreateIdempotencyKey == key {
			copy := *value
			return &copy, nil
		}
	}
	return nil, nil
}
func (r *attachmentRepoStub) Find(_ context.Context, tenant string, opportunityID uint64, id string) (*Attachment, error) {
	value := r.values[id]
	if value == nil || value.TenantID != tenant || value.OpportunityID != opportunityID {
		return nil, ErrAttachmentNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *attachmentRepoStub) FindForUpdate(_ context.Context, tenant, id string) (*Attachment, error) {
	value := r.values[id]
	if value == nil || value.TenantID != tenant {
		return nil, ErrAttachmentNotFound
	}
	copy := *value
	return &copy, nil
}
func (r *attachmentRepoStub) List(_ context.Context, tenant string, opportunityID uint64) ([]Attachment, error) {
	result := []Attachment{}
	for _, value := range r.values {
		if value.TenantID == tenant && value.OpportunityID == opportunityID {
			result = append(result, *value)
		}
	}
	return result, nil
}
func (r *attachmentRepoStub) Update(_ context.Context, value *Attachment, expected uint64, fields map[string]any) error {
	stored := r.values[value.PublicID]
	if stored == nil || stored.Version != expected {
		return ErrVersionConflict
	}
	if status, ok := fields["scan_status"].(string); ok {
		stored.ScanStatus = status
		value.ScanStatus = status
	}
	if version, ok := fields["object_version"].(string); ok {
		stored.ObjectVersion = version
		value.ObjectVersion = version
	}
	if ref, ok := fields["scan_reference"].(string); ok {
		stored.ScanReference = ref
		value.ScanReference = ref
	}
	if expires, ok := fields["upload_expires_at"].(time.Time); ok {
		stored.UploadExpiresAt = &expires
		value.UploadExpiresAt = &expires
	}
	if lease, ok := fields["upload_lease_until"].(time.Time); ok {
		stored.UploadLeaseUntil = &lease
		value.UploadLeaseUntil = &lease
	}
	if fields["upload_lease_until"] == nil {
		stored.UploadLeaseUntil = nil
		value.UploadLeaseUntil = nil
	}
	if lease, ok := fields["finalize_lease_until"].(time.Time); ok {
		stored.FinalizeLeaseUntil = &lease
		value.FinalizeLeaseUntil = &lease
	}
	if fields["finalize_lease_until"] == nil {
		stored.FinalizeLeaseUntil = nil
		value.FinalizeLeaseUntil = nil
	}
	if uploaded, ok := fields["uploaded_at"].(time.Time); ok {
		stored.UploadedAt = &uploaded
		value.UploadedAt = &uploaded
	}
	if scanned, ok := fields["scanned_at"].(time.Time); ok {
		stored.ScannedAt = &scanned
		value.ScannedAt = &scanned
	}
	stored.Version++
	value.Version = stored.Version
	r.writes++
	return nil
}

type attachmentOpportunityRepo struct{ *GORMRepository }

func (attachmentOpportunityRepo) FindByID(_ context.Context, principal auth.Principal, id uint64) (*Opportunity, error) {
	if principal.TenantID != "tenant-a" || id != 7 {
		return nil, ErrNotFound
	}
	value := &Opportunity{CustomerID: 3, ExpectedAmount: decimal.NewFromInt(1), Status: StatusFollowing}
	value.ID, value.TenantID, value.OwnerUserID = 7, "tenant-a", "actor-a"
	return value, nil
}

type attachmentStoreStub struct {
	available     bool
	finalizeCalls int
	openContent   []byte
	finalizeErr   error
}

func (s *attachmentStoreStub) Available() bool { return s.available }
func (s *attachmentStoreStub) CreateUpload(_ context.Context, key, media string, size uint64, digest, name string) (AttachmentUploadGrant, error) {
	if !s.available {
		return AttachmentUploadGrant{}, ErrAttachmentUnavailable
	}
	return AttachmentUploadGrant{URL: "https://objects.example.test/upload", ExpiresAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)}, nil
}
func (s *attachmentStoreStub) Finalize(context.Context, string) (AttachmentObjectMetadata, error) {
	s.finalizeCalls++
	if s.finalizeErr != nil {
		return AttachmentObjectMetadata{}, s.finalizeErr
	}
	return AttachmentObjectMetadata{ObjectVersion: "v1", SizeBytes: 4, MIMEType: "application/pdf", SHA256: testAttachmentSHA}, nil
}
func (s *attachmentStoreStub) OpenVerified(context.Context, string, string, string, uint64) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.openContent)), nil
}

type attachmentScannerStub struct {
	available   bool
	submissions []string
}

func (s *attachmentScannerStub) Available() bool { return s.available }
func (s *attachmentScannerStub) Submit(_ context.Context, key, object, version, digest string, size uint64, media string) (string, error) {
	s.submissions = append(s.submissions, key)
	return "scan-1", nil
}

const testAttachmentSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func attachmentContext(tenant string) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{TenantID: tenant, UserID: "actor-a", DisplayName: "Actor", ScopeMode: auth.ScopeSelf})
}
func attachmentServiceFixture(store *attachmentStoreStub, scanner *attachmentScannerStub) (*AttachmentService, *attachmentRepoStub) {
	repo := &attachmentRepoStub{values: map[string]*Attachment{}}
	service := NewAttachmentService(repo, attachmentOpportunityRepo{}, &countingAuditWriter{}, store, scanner, 4)
	service.now = func() time.Time { return time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC) }
	return service, repo
}

func TestAttachmentCreateFailsBeforePersistenceWhenTrustBoundaryUnavailable(t *testing.T) {
	service, repo := attachmentServiceFixture(&attachmentStoreStub{}, &attachmentScannerStub{})
	_, err := service.CreateUpload(attachmentContext("tenant-a"), 7, AttachmentCreateRequest{FileName: "proof.pdf", SizeBytes: 4, MIMEType: "application/pdf", SHA256: testAttachmentSHA, IdempotencyKey: "key"})
	if !errors.Is(err, ErrAttachmentUnavailable) || repo.writes != 0 || len(repo.values) != 0 {
		t.Fatalf("err=%v writes=%d values=%d", err, repo.writes, len(repo.values))
	}
}

func TestAttachmentCreatePersistsExpiryAndRejectsCrossTenantScope(t *testing.T) {
	service, repo := attachmentServiceFixture(&attachmentStoreStub{available: true}, &attachmentScannerStub{available: true})
	result, err := service.CreateUpload(attachmentContext("tenant-a"), 7, AttachmentCreateRequest{FileName: "proof.pdf", SizeBytes: 4, MIMEType: "application/pdf", SHA256: testAttachmentSHA, IdempotencyKey: "key"})
	if err != nil || result.Attachment.UploadExpiresAt == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	stored := repo.values[result.Attachment.ID]
	if stored == nil || stored.UploadExpiresAt == nil || !stored.UploadExpiresAt.Equal(result.ExpiresAt) {
		t.Fatal("expiry was not durably persisted")
	}
	if _, err = service.List(attachmentContext("tenant-b"), 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant list: %v", err)
	}
}

func TestAttachmentCompleteUsesStableScanCoordinateAndDoesNotResubmitScanning(t *testing.T) {
	store, scanner := &attachmentStoreStub{available: true}, &attachmentScannerStub{available: true}
	service, repo := attachmentServiceFixture(store, scanner)
	created, err := service.CreateUpload(attachmentContext("tenant-a"), 7, AttachmentCreateRequest{FileName: "proof.pdf", SizeBytes: 4, MIMEType: "application/pdf", SHA256: testAttachmentSHA, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteUpload(attachmentContext("tenant-a"), 7, created.Attachment.ID, AttachmentCompleteRequest{Version: created.Attachment.Version, IdempotencyKey: "complete-key"})
	if err != nil {
		t.Fatal(err)
	}
	if completed.ScanStatus != AttachmentScanning || !slices.Equal(scanner.submissions, []string{created.Attachment.ID}) {
		t.Fatalf("completed=%#v submissions=%v", completed, scanner.submissions)
	}
	if _, err = service.CompleteUpload(attachmentContext("tenant-a"), 7, created.Attachment.ID, AttachmentCompleteRequest{Version: 1, IdempotencyKey: "complete-key"}); err != nil {
		t.Fatal(err)
	}
	if len(scanner.submissions) != 1 || repo.values[created.Attachment.ID].ScanStatus != AttachmentScanning {
		t.Fatalf("duplicate submission: %v", scanner.submissions)
	}
}

func TestAttachmentCompleteFailureReleasesLeaseAndExpiredCrashLeaseCanBeTakenOver(t *testing.T) {
	store, scanner := &attachmentStoreStub{available: true, finalizeErr: errors.New("storage timeout")}, &attachmentScannerStub{available: true}
	service, repo := attachmentServiceFixture(store, scanner)
	created, err := service.CreateUpload(attachmentContext("tenant-a"), 7, AttachmentCreateRequest{FileName: "proof.pdf", SizeBytes: 4, MIMEType: "application/pdf", SHA256: testAttachmentSHA, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CompleteUpload(attachmentContext("tenant-a"), 7, created.Attachment.ID, AttachmentCompleteRequest{Version: created.Attachment.Version, IdempotencyKey: "complete"})
	if !errors.Is(err, ErrAttachmentUnavailable) || repo.values[created.Attachment.ID].ScanStatus != AttachmentPendingUpload || repo.values[created.Attachment.ID].FinalizeLeaseUntil != nil {
		t.Fatalf("failed finalize did not release: value=%#v err=%v", repo.values[created.Attachment.ID], err)
	}
	store.finalizeErr = nil
	current := repo.values[created.Attachment.ID]
	if _, err = service.CompleteUpload(attachmentContext("tenant-a"), 7, created.Attachment.ID, AttachmentCompleteRequest{Version: current.Version, IdempotencyKey: "complete"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a process crash after durable FINALIZING and verify an expired
	// owner can be taken over without a nonexistent reconciliation worker.
	crashed := repo.values[created.Attachment.ID]
	crashed.ScanStatus = AttachmentFinalizing
	crashed.ScanReference = ""
	expired := service.now().Add(-time.Second)
	crashed.FinalizeLeaseUntil = &expired
	crashed.Version++
	if _, err = service.CompleteUpload(attachmentContext("tenant-a"), 7, created.Attachment.ID, AttachmentCompleteRequest{Version: 1, IdempotencyKey: "complete"}); err != nil {
		t.Fatal(err)
	}
	if repo.values[created.Attachment.ID].ScanStatus != AttachmentScanning || len(scanner.submissions) != 2 {
		t.Fatalf("takeover failed: status=%s submissions=%v", repo.values[created.Attachment.ID].ScanStatus, scanner.submissions)
	}
}

func TestAttachmentDownloadOnlyOpensCleanVersionAndAudits(t *testing.T) {
	store, scanner := &attachmentStoreStub{available: true, openContent: []byte("safe")}, &attachmentScannerStub{available: true}
	service, repo := attachmentServiceFixture(store, scanner)
	now := service.now()
	value := &Attachment{PublicID: "file-1", OpportunityID: 7, ObjectKey: "key", ObjectVersion: "v1", FileName: "proof.pdf", SizeBytes: 4, MIMEType: "application/pdf", SHA256: testAttachmentSHA, ScanStatus: AttachmentScanning, UploadedAt: &now}
	value.ID, value.TenantID, value.Version = 1, "tenant-a", 1
	repo.values[value.PublicID] = value
	if _, err := service.Download(attachmentContext("tenant-a"), 7, value.PublicID); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("unscanned download: %v", err)
	}
	value.ScanStatus = AttachmentClean
	download, err := service.Download(attachmentContext("tenant-a"), 7, value.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(download.Reader)
	if string(body) != "safe" {
		t.Fatalf("body=%q", body)
	}
	if err = download.Complete(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

var _ audit.Writer = (*countingAuditWriter)(nil)
