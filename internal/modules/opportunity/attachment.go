package opportunity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AttachmentPendingUpload = "PENDING_UPLOAD"
	AttachmentFinalizing    = "FINALIZING"
	AttachmentScanning      = "SCANNING"
	AttachmentClean         = "CLEAN"
	AttachmentRejected      = "REJECTED"
	AttachmentScanFailed    = "SCAN_FAILED"
	defaultAttachmentMax    = uint64(20 << 20)
)

var attachmentMIMEs = map[string]string{
	"pdf":  "application/pdf",
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// Attachment stores only an immutable object reference and trust metadata.
// Binary content must never be written to CRM MySQL.
type Attachment struct {
	database.Model
	PublicID             string     `gorm:"size:64;not null;uniqueIndex:uq_opportunity_attachment_public,priority:2"`
	OpportunityID        uint64     `gorm:"not null;index:idx_opportunity_attachment,priority:2"`
	ObjectKey            string     `gorm:"size:512;not null"`
	ObjectVersion        string     `gorm:"size:256"`
	FileName             string     `gorm:"size:255;not null"`
	SizeBytes            uint64     `gorm:"not null"`
	MIMEType             string     `gorm:"size:160;not null"`
	SHA256               string     `gorm:"size:64;not null"`
	ScanStatus           string     `gorm:"size:32;not null;index"`
	ScanReference        string     `gorm:"size:128"`
	UploadExpiresAt      *time.Time `gorm:"precision:3"`
	UploadLeaseUntil     *time.Time `gorm:"precision:3"`
	FinalizeLeaseUntil   *time.Time `gorm:"precision:3"`
	UploadedAt           *time.Time `gorm:"precision:3"`
	ScannedAt            *time.Time `gorm:"precision:3"`
	CreateActorID        string     `gorm:"size:64;not null;uniqueIndex:uq_opportunity_attachment_create,priority:2"`
	CreateIdempotencyKey string     `gorm:"size:128;not null;uniqueIndex:uq_opportunity_attachment_create,priority:3"`
	CreateRequestHash    string     `gorm:"size:64;not null"`
}

func (Attachment) TableName() string { return "crm_opportunity_attachments" }

type AttachmentCreateRequest struct {
	FileName       string `json:"file_name" binding:"required,max=255"`
	SizeBytes      uint64 `json:"size_bytes" binding:"required"`
	MIMEType       string `json:"mime_type" binding:"required,max=160"`
	SHA256         string `json:"sha256" binding:"required,len=64"`
	IdempotencyKey string `json:"-"`
}

type AttachmentCompleteRequest struct {
	Version        uint64 `json:"version" binding:"required"`
	IdempotencyKey string `json:"-"`
}

type AttachmentScanEvent struct {
	AttachmentID  string    `json:"attachment_id" binding:"required,max=64"`
	ScanReference string    `json:"scan_reference" binding:"required,max=128"`
	Status        string    `json:"status" binding:"required,max=32"`
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`
}

type AttachmentResponse struct {
	ID              string     `json:"id"`
	OpportunityID   uint64     `json:"opportunity_id"`
	FileName        string     `json:"file_name"`
	SizeBytes       uint64     `json:"size_bytes"`
	MIMEType        string     `json:"mime_type"`
	SHA256          string     `json:"sha256"`
	ScanStatus      string     `json:"scan_status"`
	UploadExpiresAt *time.Time `json:"upload_expires_at,omitempty"`
	UploadedAt      *time.Time `json:"uploaded_at,omitempty"`
	ScannedAt       *time.Time `json:"scanned_at,omitempty"`
	Version         uint64     `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
}

type AttachmentUploadSessionResponse struct {
	Attachment AttachmentResponse `json:"attachment"`
	UploadURL  string             `json:"upload_url"`
	ExpiresAt  time.Time          `json:"expires_at"`
}

type AttachmentCapabilities struct {
	UploadAvailable   bool     `json:"upload_available"`
	DownloadAvailable bool     `json:"download_available"`
	ScannerAvailable  bool     `json:"scanner_available"`
	MaxSizeBytes      uint64   `json:"max_size_bytes"`
	AllowedMIMETypes  []string `json:"allowed_mime_types"`
}

type AttachmentObjectMetadata struct {
	ObjectVersion string
	SizeBytes     uint64
	MIMEType      string
	SHA256        string
}

type AttachmentUploadGrant struct {
	URL       string
	ExpiresAt time.Time
}

// AttachmentObjectStore is an immutable object-store boundary. Finalize must
// bind a version/etag so content cannot be replaced after it has been scanned.
type AttachmentObjectStore interface {
	Available() bool
	CreateUpload(context.Context, string, string, uint64, string, string) (AttachmentUploadGrant, error)
	Finalize(context.Context, string) (AttachmentObjectMetadata, error)
	OpenVerified(context.Context, string, string, string, uint64) (io.ReadCloser, error)
}

type AttachmentScanner interface {
	Available() bool
	// Submit must use idempotencyKey as an exactly-once business coordinate.
	// The same key and object tuple must return the original scan reference;
	// the same key with a different tuple must fail.
	Submit(context.Context, string, string, string, string, uint64, string) (string, error)
}

type UnavailableAttachmentObjectStore struct{}

func (UnavailableAttachmentObjectStore) Available() bool { return false }
func (UnavailableAttachmentObjectStore) CreateUpload(context.Context, string, string, uint64, string, string) (AttachmentUploadGrant, error) {
	return AttachmentUploadGrant{}, ErrAttachmentUnavailable
}
func (UnavailableAttachmentObjectStore) Finalize(context.Context, string) (AttachmentObjectMetadata, error) {
	return AttachmentObjectMetadata{}, ErrAttachmentUnavailable
}
func (UnavailableAttachmentObjectStore) OpenVerified(context.Context, string, string, string, uint64) (io.ReadCloser, error) {
	return nil, ErrAttachmentUnavailable
}

type UnavailableAttachmentScanner struct{}

func (UnavailableAttachmentScanner) Available() bool { return false }
func (UnavailableAttachmentScanner) Submit(context.Context, string, string, string, string, uint64, string) (string, error) {
	return "", ErrAttachmentUnavailable
}

type AttachmentRepository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	Create(context.Context, *Attachment) error
	FindCreate(context.Context, string, string, string) (*Attachment, error)
	Find(context.Context, string, uint64, string) (*Attachment, error)
	FindForUpdate(context.Context, string, string) (*Attachment, error)
	List(context.Context, string, uint64) ([]Attachment, error)
	Update(context.Context, *Attachment, uint64, map[string]any) error
}

type GORMAttachmentRepository struct{ db *gorm.DB }

func NewGORMAttachmentRepository(db *gorm.DB) *GORMAttachmentRepository {
	return &GORMAttachmentRepository{db: db}
}
func (r *GORMAttachmentRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}
func (r *GORMAttachmentRepository) Create(ctx context.Context, value *Attachment) error {
	return database.FromContext(ctx, r.db).Create(value).Error
}
func (r *GORMAttachmentRepository) FindCreate(ctx context.Context, tenant, actor, key string) (*Attachment, error) {
	var value Attachment
	err := database.FromContext(ctx, r.db).Where("tenant_id=? AND create_actor_id=? AND create_idempotency_key=? AND deleted_at IS NULL", tenant, actor, key).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &value, err
}
func (r *GORMAttachmentRepository) Find(ctx context.Context, tenant string, opportunityID uint64, id string) (*Attachment, error) {
	var value Attachment
	err := database.FromContext(ctx, r.db).Where("tenant_id=? AND opportunity_id=? AND public_id=? AND deleted_at IS NULL", tenant, opportunityID, id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAttachmentNotFound
	}
	return &value, err
}
func (r *GORMAttachmentRepository) FindForUpdate(ctx context.Context, tenant, id string) (*Attachment, error) {
	var value Attachment
	err := database.FromContext(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND public_id=? AND deleted_at IS NULL", tenant, id).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAttachmentNotFound
	}
	return &value, err
}
func (r *GORMAttachmentRepository) List(ctx context.Context, tenant string, opportunityID uint64) ([]Attachment, error) {
	var values []Attachment
	err := database.FromContext(ctx, r.db).Where("tenant_id=? AND opportunity_id=? AND deleted_at IS NULL", tenant, opportunityID).Order("created_at DESC,id DESC").Limit(200).Find(&values).Error
	return values, err
}
func (r *GORMAttachmentRepository) Update(ctx context.Context, value *Attachment, expected uint64, fields map[string]any) error {
	fields["updated_at"], fields["updated_by"], fields["version"] = time.Now().UTC(), value.UpdatedBy, gorm.Expr("version+1")
	result := database.FromContext(ctx, r.db).Model(&Attachment{}).Where("id=? AND tenant_id=? AND version=? AND deleted_at IS NULL", value.ID, value.TenantID, expected).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	value.Version = expected + 1
	return nil
}

type AttachmentService struct {
	repo          AttachmentRepository
	opportunities Repository
	audit         audit.Writer
	store         AttachmentObjectStore
	scanner       AttachmentScanner
	maxBytes      uint64
	now           func() time.Time
}

const attachmentSideEffectLease = 30 * time.Second

func NewAttachmentService(repo AttachmentRepository, opportunities Repository, writer audit.Writer, store AttachmentObjectStore, scanner AttachmentScanner, maxBytes uint64) *AttachmentService {
	if store == nil {
		store = UnavailableAttachmentObjectStore{}
	}
	if scanner == nil {
		scanner = UnavailableAttachmentScanner{}
	}
	if maxBytes == 0 {
		maxBytes = defaultAttachmentMax
	}
	return &AttachmentService{repo: repo, opportunities: opportunities, audit: writer, store: store, scanner: scanner, maxBytes: maxBytes, now: func() time.Time { return time.Now().UTC() }}
}

func (s *AttachmentService) Capabilities(ctx context.Context, opportunityID uint64) (AttachmentCapabilities, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return AttachmentCapabilities{}, err
	}
	if _, err = s.opportunities.FindByID(ctx, principal, opportunityID); err != nil {
		return AttachmentCapabilities{}, err
	}
	types := make([]string, 0, len(attachmentMIMEs))
	seen := map[string]bool{}
	for _, value := range attachmentMIMEs {
		if !seen[value] {
			seen[value] = true
			types = append(types, value)
		}
	}
	return AttachmentCapabilities{UploadAvailable: s.store.Available() && s.scanner.Available(), DownloadAvailable: s.store.Available(), ScannerAvailable: s.scanner.Available(), MaxSizeBytes: s.maxBytes, AllowedMIMETypes: types}, nil
}

func (s *AttachmentService) CreateUpload(ctx context.Context, opportunityID uint64, input AttachmentCreateRequest) (*AttachmentUploadSessionResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.opportunities.FindByID(ctx, principal, opportunityID); err != nil {
		return nil, err
	}
	name, mediaType, digest, err := validateAttachmentInput(input, s.maxBytes)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(key) > 128 {
		return nil, ErrIdempotencyKeyTooLong
	}
	hash := attachmentRequestHash(opportunityID, name, input.SizeBytes, mediaType, digest)
	// Do not persist an unusable session: production defaults deliberately fail
	// before metadata is written until both trust-boundary adapters are wired.
	if !s.store.Available() || !s.scanner.Available() {
		return nil, ErrAttachmentUnavailable
	}
	value, err := s.repo.FindCreate(ctx, principal.TenantID, principal.UserID, key)
	if err != nil {
		return nil, err
	}
	if value != nil && value.CreateRequestHash != hash {
		return nil, ErrIdempotencyConflict
	}
	created := false
	if value == nil {
		now := s.now()
		publicID := request.NewID()
		leaseUntil := now.Add(attachmentSideEffectLease)
		value = &Attachment{PublicID: publicID, OpportunityID: opportunityID, ObjectKey: "crm/opportunities/" + principal.TenantID + "/" + uintString(opportunityID) + "/" + publicID, FileName: name, SizeBytes: input.SizeBytes, MIMEType: mediaType, SHA256: digest, ScanStatus: AttachmentPendingUpload, CreateActorID: principal.UserID, CreateIdempotencyKey: key, CreateRequestHash: hash}
		value.UploadLeaseUntil = &leaseUntil
		value.TenantID, value.CreatedBy, value.UpdatedBy, value.CreatedAt, value.UpdatedAt, value.Version = principal.TenantID, principal.UserID, principal.UserID, now, now, 1
		err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
			if createErr := s.repo.Create(tx, value); createErr != nil {
				return createErr
			}
			return s.audit.Write(tx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_attachment", Operation: "UPLOAD_SESSION_CREATED", ResourceType: "opportunity_attachment", ResourceID: publicID, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toAttachment(value)), Result: "SUCCESS"})
		})
		if err != nil {
			if !isDuplicateAttachmentCreate(err) {
				return nil, err
			}
			winner, findErr := s.repo.FindCreate(ctx, principal.TenantID, principal.UserID, key)
			if findErr != nil {
				return nil, findErr
			}
			if winner == nil || winner.OpportunityID != opportunityID || winner.CreateActorID != principal.UserID || winner.CreateRequestHash != hash {
				return nil, ErrIdempotencyConflict
			}
			value = winner
		} else {
			created = true
		}
	}
	now := s.now()
	if !created {
		if value.UploadLeaseUntil != nil && value.UploadLeaseUntil.After(now) {
			return nil, ErrAttachmentNotReady
		}
		leaseUntil := now.Add(attachmentSideEffectLease)
		value.UpdatedBy = principal.UserID
		if err = s.repo.Update(ctx, value, value.Version, map[string]any{"upload_lease_until": leaseUntil}); err != nil {
			return nil, err
		}
		value.UploadLeaseUntil = &leaseUntil
	}
	grant, err := s.store.CreateUpload(ctx, value.ObjectKey, value.MIMEType, value.SizeBytes, value.SHA256, value.FileName)
	if err != nil {
		s.releaseUploadLease(ctx, value, principal.UserID)
		return nil, ErrAttachmentUnavailable
	}
	if grant.ExpiresAt.Before(s.now()) || strings.TrimSpace(grant.URL) == "" {
		s.releaseUploadLease(ctx, value, principal.UserID)
		return nil, ErrAttachmentUnavailable
	}
	expected := value.Version
	value.UpdatedBy = principal.UserID
	if err = s.repo.Update(ctx, value, expected, map[string]any{"upload_expires_at": grant.ExpiresAt, "upload_lease_until": nil}); err != nil {
		return nil, err
	}
	value.UploadExpiresAt, value.UploadLeaseUntil = &grant.ExpiresAt, nil
	return &AttachmentUploadSessionResponse{Attachment: toAttachment(value), UploadURL: grant.URL, ExpiresAt: grant.ExpiresAt}, nil
}

func (s *AttachmentService) CompleteUpload(ctx context.Context, opportunityID uint64, id string, input AttachmentCompleteRequest) (*AttachmentResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.opportunities.FindByID(ctx, principal, opportunityID); err != nil {
		return nil, err
	}
	value, err := s.repo.Find(ctx, principal.TenantID, opportunityID, id)
	if err != nil {
		return nil, err
	}
	if value.ScanStatus == AttachmentScanning || value.ScanStatus == AttachmentClean {
		result := toAttachment(value)
		return &result, nil
	}
	if value.ScanStatus == AttachmentFinalizing {
		if value.FinalizeLeaseUntil != nil && value.FinalizeLeaseUntil.After(s.now()) {
			return nil, ErrAttachmentNotReady
		}
	} else if value.ScanStatus != AttachmentPendingUpload {
		return nil, ErrAttachmentNotReady
	}
	if value.ScanStatus == AttachmentPendingUpload && value.Version != input.Version {
		return nil, ErrVersionConflict
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return nil, ErrIdempotencyRequired
	}
	if len(input.IdempotencyKey) > 128 {
		return nil, ErrIdempotencyKeyTooLong
	}
	if !s.store.Available() || !s.scanner.Available() {
		return nil, ErrAttachmentUnavailable
	}
	// Claim finalization before any slow external call. A crashed or concurrent
	// request may resume FINALIZING; Finalize and scanner Submit are required to
	// be idempotent, with PublicID as the stable scanner coordinate.
	expected := value.Version
	leaseUntil := s.now().Add(attachmentSideEffectLease)
	value.UpdatedBy = principal.UserID
	if err = s.repo.Update(ctx, value, expected, map[string]any{"scan_status": AttachmentFinalizing, "finalize_lease_until": leaseUntil}); err != nil {
		return nil, err
	}
	value.ScanStatus, value.FinalizeLeaseUntil = AttachmentFinalizing, &leaseUntil
	metadata, err := s.store.Finalize(ctx, value.ObjectKey)
	if err != nil {
		s.releaseFinalizeLease(ctx, value, principal.UserID)
		return nil, ErrAttachmentUnavailable
	}
	if strings.TrimSpace(metadata.ObjectVersion) == "" || metadata.SizeBytes != value.SizeBytes || canonicalMIME(metadata.MIMEType) != value.MIMEType || !strings.EqualFold(metadata.SHA256, value.SHA256) {
		s.releaseFinalizeLease(ctx, value, principal.UserID)
		return nil, ErrAttachmentInvalid
	}
	scanRef, err := s.scanner.Submit(ctx, value.PublicID, value.ObjectKey, metadata.ObjectVersion, value.SHA256, value.SizeBytes, value.MIMEType)
	if err != nil || strings.TrimSpace(scanRef) == "" {
		s.releaseFinalizeLease(ctx, value, principal.UserID)
		return nil, ErrAttachmentUnavailable
	}
	now := s.now()
	value.UpdatedBy = principal.UserID
	value.ObjectVersion, value.ScanReference, value.ScanStatus, value.UploadedAt = metadata.ObjectVersion, scanRef, AttachmentScanning, &now
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, lockErr := s.repo.FindForUpdate(tx, principal.TenantID, value.PublicID)
		if lockErr != nil {
			return lockErr
		}
		if locked.OpportunityID != opportunityID {
			return ErrAttachmentNotFound
		}
		if locked.ScanStatus == AttachmentScanning || locked.ScanStatus == AttachmentClean {
			value = locked
			return nil
		}
		if locked.ScanStatus != AttachmentFinalizing {
			return ErrAttachmentNotReady
		}
		locked.UpdatedBy = principal.UserID
		if updateErr := s.repo.Update(tx, locked, locked.Version, map[string]any{"object_version": metadata.ObjectVersion, "scan_reference": scanRef, "scan_status": AttachmentScanning, "uploaded_at": now, "finalize_lease_until": nil}); updateErr != nil {
			return updateErr
		}
		value = locked
		return s.audit.Write(tx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_attachment", Operation: "SCAN_REQUESTED", ResourceType: "opportunity_attachment", ResourceID: value.PublicID, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(toAttachment(value)), Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	result := toAttachment(value)
	return &result, nil
}

func (s *AttachmentService) List(ctx context.Context, opportunityID uint64) ([]AttachmentResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.opportunities.FindByID(ctx, principal, opportunityID); err != nil {
		return nil, err
	}
	values, err := s.repo.List(ctx, principal.TenantID, opportunityID)
	if err != nil {
		return nil, err
	}
	result := make([]AttachmentResponse, 0, len(values))
	for i := range values {
		result = append(result, toAttachment(&values[i]))
	}
	return result, nil
}

func (s *AttachmentService) ApplyScan(ctx context.Context, event AttachmentScanEvent) (*AttachmentResponse, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	status := strings.ToUpper(strings.TrimSpace(event.Status))
	if status != AttachmentClean && status != AttachmentRejected && status != AttachmentScanFailed {
		return nil, ErrAttachmentInvalid
	}
	var result AttachmentResponse
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, findErr := s.repo.FindForUpdate(tx, principal.TenantID, strings.TrimSpace(event.AttachmentID))
		if findErr != nil {
			return findErr
		}
		if value.ScanReference != strings.TrimSpace(event.ScanReference) {
			return ErrAttachmentNotFound
		}
		if value.ScanStatus == status {
			result = toAttachment(value)
			return nil
		}
		if value.ScanStatus != AttachmentScanning {
			return ErrAttachmentNotReady
		}
		if event.OccurredAt.After(s.now().Add(5*time.Minute)) || value.UploadedAt == nil || event.OccurredAt.Before(value.UploadedAt.Add(-time.Second)) {
			return ErrAttachmentInvalid
		}
		now := event.OccurredAt.UTC()
		value.UpdatedBy, value.ScanStatus, value.ScannedAt = principal.UserID, status, &now
		if updateErr := s.repo.Update(tx, value, value.Version, map[string]any{"scan_status": status, "scanned_at": now}); updateErr != nil {
			return updateErr
		}
		operation := "SCAN_REJECTED"
		if status == AttachmentClean {
			operation = "SCAN_PASSED"
		} else if status == AttachmentScanFailed {
			operation = "SCAN_FAILED"
		}
		result = toAttachment(value)
		return s.audit.Write(tx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_attachment", Operation: operation, ResourceType: "opportunity_attachment", ResourceID: value.PublicID, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(result), Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

type AttachmentDownload struct {
	Attachment AttachmentResponse
	Reader     io.ReadCloser
	complete   func(context.Context, bool) error
}

func (d *AttachmentDownload) Complete(ctx context.Context, success bool) error {
	if d == nil || d.complete == nil {
		return nil
	}
	return d.complete(ctx, success)
}

func (s *AttachmentService) Download(ctx context.Context, opportunityID uint64, id string) (*AttachmentDownload, error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.opportunities.FindByID(ctx, principal, opportunityID); err != nil {
		return nil, err
	}
	value, err := s.repo.Find(ctx, principal.TenantID, opportunityID, id)
	if err != nil {
		return nil, err
	}
	if value.ScanStatus == AttachmentRejected {
		return nil, ErrAttachmentRejected
	}
	if value.ScanStatus != AttachmentClean || value.ObjectVersion == "" {
		return nil, ErrAttachmentNotReady
	}
	if !s.store.Available() {
		return nil, ErrAttachmentUnavailable
	}
	if err = s.audit.Write(ctx, audit.Event{TenantID: principal.TenantID, Module: "opportunity_attachment", Operation: "DOWNLOAD_AUTHORIZED", ResourceType: "opportunity_attachment", ResourceID: value.PublicID, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, Result: "SUCCESS"}); err != nil {
		return nil, ErrAttachmentUnavailable
	}
	reader, err := s.store.OpenVerified(ctx, value.ObjectKey, value.ObjectVersion, value.SHA256, value.SizeBytes)
	if err != nil {
		return nil, ErrAttachmentUnavailable
	}
	download := &AttachmentDownload{Attachment: toAttachment(value), Reader: reader}
	download.complete = func(done context.Context, success bool) error {
		operation, result := "DOWNLOAD_FAILED", "FAILED"
		if success {
			operation, result = "DOWNLOAD_SUCCEEDED", "SUCCESS"
		}
		return s.audit.Write(done, audit.Event{TenantID: principal.TenantID, Module: "opportunity_attachment", Operation: operation, ResourceType: "opportunity_attachment", ResourceID: value.PublicID, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, Result: result})
	}
	return download, nil
}

func validateAttachmentInput(input AttachmentCreateRequest, max uint64) (string, string, string, error) {
	name := strings.TrimSpace(input.FileName)
	if name == "" || len([]byte(name)) > 255 || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", "", "", ErrAttachmentInvalid
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", "", "", ErrAttachmentInvalid
		}
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	expected, ok := attachmentMIMEs[ext]
	mediaType := canonicalMIME(input.MIMEType)
	if !ok || mediaType != expected || input.SizeBytes == 0 || input.SizeBytes > max {
		return "", "", "", ErrAttachmentInvalid
	}
	digest := strings.ToLower(strings.TrimSpace(input.SHA256))
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", "", "", ErrAttachmentInvalid
	}
	return name, mediaType, digest, nil
}
func canonicalMIME(value string) string {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed)
}
func attachmentRequestHash(id uint64, name string, size uint64, mediaType, digest string) string {
	body, _ := json.Marshal([]any{id, name, size, mediaType, digest})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func isDuplicateAttachmentCreate(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func (s *AttachmentService) releaseUploadLease(ctx context.Context, value *Attachment, actor string) {
	value.UpdatedBy = actor
	if s.repo.Update(ctx, value, value.Version, map[string]any{"upload_lease_until": nil}) == nil {
		value.UploadLeaseUntil = nil
	}
}

func (s *AttachmentService) releaseFinalizeLease(ctx context.Context, value *Attachment, actor string) {
	value.UpdatedBy = actor
	if s.repo.Update(ctx, value, value.Version, map[string]any{"scan_status": AttachmentPendingUpload, "finalize_lease_until": nil}) == nil {
		value.ScanStatus, value.FinalizeLeaseUntil = AttachmentPendingUpload, nil
	}
}
func toAttachment(value *Attachment) AttachmentResponse {
	return AttachmentResponse{ID: value.PublicID, OpportunityID: value.OpportunityID, FileName: value.FileName, SizeBytes: value.SizeBytes, MIMEType: value.MIMEType, SHA256: value.SHA256, ScanStatus: value.ScanStatus, UploadExpiresAt: value.UploadExpiresAt, UploadedAt: value.UploadedAt, ScannedAt: value.ScannedAt, Version: value.Version, CreatedAt: value.CreatedAt}
}
