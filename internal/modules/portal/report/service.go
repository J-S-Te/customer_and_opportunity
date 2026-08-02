package report

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/workerruntime"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var (
	ErrNotFound             = apperror.New(http.StatusNotFound, "PORTAL_REPORT_NOT_FOUND", "report request not found")
	ErrInvalidRequest       = apperror.New(http.StatusBadRequest, "PORTAL_REPORT_INVALID_REQUEST", "report request is invalid")
	ErrProjectNotAccessible = apperror.New(http.StatusForbidden, "PORTAL_REPORT_PROJECT_FORBIDDEN", "project is not accessible")
	ErrIdempotencyConflict  = apperror.New(http.StatusConflict, "PORTAL_REPORT_IDEMPOTENCY_CONFLICT", "idempotency key was used with another request")
	ErrInvalidTransition    = apperror.New(http.StatusConflict, "PORTAL_REPORT_INVALID_TRANSITION", "report request transition is not allowed")
	ErrInvalidCallback      = apperror.New(http.StatusUnprocessableEntity, "PORTAL_REPORT_INVALID_CALLBACK", "report callback is invalid")
	ErrCallbackConflict     = apperror.New(http.StatusConflict, "PORTAL_REPORT_CALLBACK_CONFLICT", "report callback conflicts with the accepted callback version")
	ErrNotificationNotFound = apperror.New(http.StatusNotFound, "PORTAL_REPORT_NOTIFICATION_NOT_FOUND", "report notification not found")
	ErrDeliveryUnavailable  = apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_DELIVERY_WORKER_UNAVAILABLE", "report request delivery is temporarily unavailable")
)

const maxReportFileSize = 50 << 20

const (
	maxTenantIDBytes            = 64
	maxAccountIDBytes           = 128
	maxProjectIDBytes           = 64
	maxReportTypeBytes          = 64
	maxReportReasonBytes        = 2000
	maxReceiveEmailBytes        = 320
	maxIdempotencyKeyBytes      = 128
	maxDownstreamRequestIDBytes = 128
	maxApprovalResultBytes      = 2000
)

var sha256HexPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

type Actor struct {
	TenantID   string
	CustomerID uint64
	AccountID  string
}
type CreateCommand struct{ ProjectID, ReportType, Reason, ReceiveEmail, IdempotencyKey string }
type Callback struct {
	IdempotencyKey      string `json:"-"`
	TenantID            string `json:"tenant_id"`
	RequestID           uint64 `json:"request_id"`
	CustomerID          uint64 `json:"customer_id"`
	ProjectID           string `json:"project_id"`
	Version             uint64 `json:"version"`
	Status              Status `json:"status"`
	DownstreamRequestID string `json:"downstream_request_id"`
	ApprovalResult      string `json:"approval_result"`
	ObjectRef           string `json:"object_ref"`
	FileName            string `json:"file_name"`
	MIME                string `json:"mime"`
	FileHash            string `json:"file_hash"`
	Size                int64  `json:"size"`
}

type FileDescriptor struct {
	ObjectRef string
	FileName  string
	MIME      string
	FileHash  string
	Size      int64
}

// IngestResult contains only metadata produced by the trusted Portal-owned
// ingestor. Encryption key references are never accepted from a callback.
type IngestResult struct {
	ObjectKeyCipher     []byte
	ObjectVersion       string
	EncryptionKeyRef    string
	EncryptionAlgorithm string
	ScanStatus          string
	ScanReference       string
	ScannedAt           time.Time
	WatermarkStatus     string
}

type ProjectAccess interface {
	Accessible(context.Context, string, uint64, string) (bool, error)
}
type EmailProtector interface {
	Encrypt(context.Context, string) ([]byte, error)
}
type DescriptorProtector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}
type FileIngestor interface {
	// eventID is a stable idempotency identity and must be bound to the exact
	// descriptor by every provider implementation.
	Ingest(context.Context, string, FileDescriptor) (IngestResult, error)
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID() string }
type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	Create(context.Context, *Request) error
	List(context.Context, string, uint64, int, int) (pagination.Page[Request], error)
	Find(context.Context, string, uint64, uint64) (*Request, error)
	FindByIdempotencyKey(context.Context, string, uint64, string) (*Request, error)
	FindForUpdate(context.Context, string, uint64) (*Request, error)
	Update(context.Context, *Request, uint64, map[string]any) error
	CreateFile(context.Context, *File) error
	CreateIngestJob(context.Context, *IngestJob) error
	FindIngestJobForUpdate(context.Context, uint64) (*IngestJob, error)
	UpdateIngestJob(context.Context, *IngestJob, map[string]any) error
	CreateOutbox(context.Context, *Outbox) error
	CreateStatusEvent(context.Context, *StatusEvent) error
	FindStatusEventBySource(context.Context, string, uint64, string) (*StatusEvent, error)
	ListStatusEvents(context.Context, string, uint64, uint64) ([]StatusEvent, error)
	CreateNotification(context.Context, *Notification) error
	ListNotifications(context.Context, Actor, bool, int, int) (pagination.Page[NotificationView], error)
	CountUnreadNotifications(context.Context, Actor) (int64, error)
	FindNotificationForUpdate(context.Context, Actor, uint64) (*Notification, error)
	MarkNotificationRead(context.Context, *Notification, time.Time) error
	CreateNotificationReadEvent(context.Context, *NotificationReadEvent) error
}

func (s *Service) List(ctx context.Context, actor Actor, page, pageSize int) (pagination.Page[Request], error) {
	if actor.CustomerID == 0 || strings.TrimSpace(actor.TenantID) == "" {
		return pagination.Page[Request]{}, ErrNotFound
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	return s.repo.List(ctx, actor.TenantID, actor.CustomerID, page, pageSize)
}
func (s *Service) Get(ctx context.Context, actor Actor, id uint64) (*Request, error) {
	if strings.TrimSpace(actor.TenantID) == "" || actor.CustomerID == 0 || id == 0 {
		return nil, ErrNotFound
	}
	return s.repo.Find(ctx, actor.TenantID, actor.CustomerID, id)
}

type Detail struct {
	Request *Request
	Events  []StatusEvent
}

// NotificationView deliberately excludes tenant/customer/account identity,
// encrypted email and object-storage metadata.
type NotificationView struct {
	ID         uint64     `json:"id"`
	RequestID  uint64     `json:"request_id"`
	RequestNo  string     `json:"request_no"`
	ReportType string     `json:"report_type"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
}

func (s *Service) ListNotifications(ctx context.Context, actor Actor, unreadOnly bool, page, pageSize int) (pagination.Page[NotificationView], error) {
	if !validNotificationActor(actor) {
		return pagination.Page[NotificationView]{}, ErrNotificationNotFound
	}
	page, pageSize = pagination.Normalize(page, pageSize)
	return s.repo.ListNotifications(ctx, actor, unreadOnly, page, pageSize)
}

func (s *Service) UnreadNotificationCount(ctx context.Context, actor Actor) (int64, error) {
	if !validNotificationActor(actor) {
		return 0, ErrNotificationNotFound
	}
	return s.repo.CountUnreadNotifications(ctx, actor)
}

func (s *Service) ReadNotification(ctx context.Context, actor Actor, id uint64) error {
	if !validNotificationActor(actor) || id == 0 {
		return ErrNotificationNotFound
	}
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, err := s.repo.FindNotificationForUpdate(tx, actor, id)
		if err != nil {
			return err
		}
		if value.Status == NotificationRead {
			return nil
		}
		if value.Status != NotificationUnread {
			return ErrNotificationNotFound
		}
		now := s.clock.Now().UTC()
		if err = s.repo.MarkNotificationRead(tx, value, now); err != nil {
			return err
		}
		return s.repo.CreateNotificationReadEvent(tx, &NotificationReadEvent{
			TenantID: value.TenantID, CustomerID: value.CustomerID, NotificationID: value.ID,
			RequestID: value.RequestID, AccountID: value.AccountID,
			RequestTrace: strings.TrimSpace(requestctx.ID(tx)), OccurredAt: now,
		})
	})
}

func validNotificationActor(actor Actor) bool {
	return validBoundedText(strings.TrimSpace(actor.TenantID), maxTenantIDBytes) && actor.CustomerID > 0 &&
		validBoundedText(strings.TrimSpace(actor.AccountID), maxAccountIDBytes)
}

// GetDetail reads the aggregate and its append-only timeline through the same
// tenant/customer scope. Status updates and their event are committed in one
// transaction; both detail reads use the same transaction handle as well.
func (s *Service) GetDetail(ctx context.Context, actor Actor, id uint64) (*Detail, error) {
	if strings.TrimSpace(actor.TenantID) == "" || actor.CustomerID == 0 || id == 0 {
		return nil, ErrNotFound
	}
	var detail Detail
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, err := s.repo.Find(tx, actor.TenantID, actor.CustomerID, id)
		if err != nil {
			return err
		}
		events, err := s.repo.ListStatusEvents(tx, actor.TenantID, actor.CustomerID, id)
		if err != nil {
			return err
		}
		detail.Request, detail.Events = value, events
		return nil
	})
	return &detail, err
}

type Service struct {
	repo         Repository
	projects     ProjectAccess
	emails       EmailProtector
	ingest       DescriptorProtector
	clock        Clock
	ids          IDGenerator
	readiness    workerruntime.Readiness
	workerMaxAge time.Duration
}

func NewService(repo Repository, projects ProjectAccess, emails EmailProtector, ingest DescriptorProtector, clock Clock, ids IDGenerator) *Service {
	return &Service{repo: repo, projects: projects, emails: emails, ingest: ingest, clock: clock, ids: ids}
}

// UseWorkerReadiness installs persisted liveness evidence for the independent
// delivery worker. Configuration alone must never enable new submissions.
func (s *Service) UseWorkerReadiness(readiness workerruntime.Readiness, maxAge time.Duration) *Service {
	s.readiness, s.workerMaxAge = readiness, maxAge
	return s
}

func (s *Service) deliveryWorkerReady(ctx context.Context) bool {
	// Embedders that do not install admission control keep the historical
	// service behavior. The production Portal bootstrap always installs it.
	if s.readiness == nil {
		return true
	}
	if s.clock == nil || s.workerMaxAge <= 0 {
		return false
	}
	ready, err := s.readiness.HasFreshHeartbeat(ctx, workerruntime.ReportDeliveryWorker, s.clock.Now().UTC().Add(-s.workerMaxAge))
	return err == nil && ready
}

func (s *Service) Create(ctx context.Context, actor Actor, cmd CreateCommand) (*Request, error) {
	actor.TenantID = strings.TrimSpace(actor.TenantID)
	actor.AccountID = strings.TrimSpace(actor.AccountID)
	cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
	cmd.ReportType = strings.TrimSpace(cmd.ReportType)
	cmd.Reason = strings.TrimSpace(cmd.Reason)
	cmd.ReceiveEmail = strings.TrimSpace(cmd.ReceiveEmail)
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if !validBoundedText(actor.TenantID, maxTenantIDBytes) || actor.CustomerID == 0 || !validBoundedText(actor.AccountID, maxAccountIDBytes) ||
		!validBoundedText(cmd.ProjectID, maxProjectIDBytes) || !validBoundedText(cmd.ReportType, maxReportTypeBytes) ||
		!validNarrative(cmd.Reason, maxReportReasonBytes) || !validBoundedText(cmd.IdempotencyKey, maxIdempotencyKeyBytes) || !validEmail(cmd.ReceiveEmail) {
		return nil, ErrInvalidRequest
	}
	allowed, err := s.projects.Accessible(ctx, actor.TenantID, actor.CustomerID, cmd.ProjectID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrProjectNotAccessible
	}
	hash := requestHash(cmd)
	if existing, err := s.repo.FindByIdempotencyKey(ctx, actor.TenantID, actor.CustomerID, cmd.IdempotencyKey); err == nil {
		if !sameCreateBinding(existing, actor, cmd) || existing.RequestHash != hash {
			return nil, ErrIdempotencyConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !s.deliveryWorkerReady(ctx) {
		return nil, ErrDeliveryUnavailable
	}
	emailCipher, err := s.emails.Encrypt(ctx, strings.TrimSpace(cmd.ReceiveEmail))
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	requestNo := strings.TrimSpace(s.ids.NewID())
	if !validBoundedText(requestNo, 32) {
		return nil, errors.New("generated report request identifier is invalid")
	}
	value := &Request{ActorModel: ActorModel{TenantID: actor.TenantID, CreatedBy: actor.AccountID, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1}, RequestNo: requestNo, ProjectID: cmd.ProjectID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, ReportType: cmd.ReportType, Reason: cmd.Reason, ReceiveEmailCipher: emailCipher, Status: StatusSubmitted, SubmittedAt: now, IdempotencyKey: cmd.IdempotencyKey, RequestHash: hash}
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if existing, findErr := s.repo.FindByIdempotencyKey(tx, actor.TenantID, actor.CustomerID, cmd.IdempotencyKey); findErr == nil {
			if !sameCreateBinding(existing, actor, cmd) || existing.RequestHash != hash {
				return ErrIdempotencyConflict
			}
			value = existing
			return nil
		} else if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if !s.deliveryWorkerReady(tx) {
			return ErrDeliveryUnavailable
		}
		if e := s.repo.Create(tx, value); e != nil {
			return e
		}
		if e := s.repo.CreateStatusEvent(tx, statusEvent(value, "REPORT_SUBMITTED", 1, "", StatusSubmitted, "CUSTOMER", actor.AccountID, "CREATE", cmd.IdempotencyKey, hash, requestctx.ID(tx), now)); e != nil {
			return e
		}
		// Create assigns the database ID, so the outbox payload must be encoded
		// inside the transaction after the insert.
		payload, marshalErr := json.Marshal(map[string]any{
			"request_id": value.ID, "request_no": value.RequestNo, "customer_id": value.CustomerID,
			"project_id": value.ProjectID, "report_type": value.ReportType, "reason": value.Reason,
		})
		if marshalErr != nil {
			return marshalErr
		}
		return s.repo.CreateOutbox(tx, &Outbox{EventID: s.ids.NewID(), TenantID: actor.TenantID, EventType: "PORTAL_REPORT_SUBMITTED", AggregateID: value.ID, Payload: payload, Status: "PENDING", CreatedAt: now})
	})
	if err == nil {
		return value, nil
	}
	// A concurrent request using the same key can win the database unique
	// constraint between the fast lookup and insert. Return that committed
	// aggregate only when its canonical request hash matches.
	if existing, findErr := s.repo.FindByIdempotencyKey(ctx, actor.TenantID, actor.CustomerID, cmd.IdempotencyKey); findErr == nil {
		if !sameCreateBinding(existing, actor, cmd) || existing.RequestHash != hash {
			return nil, ErrIdempotencyConflict
		}
		return existing, nil
	}
	return value, err
}

// MarkApprovalStarted moves the local projection only after the project
// service has durably accepted the idempotent submission. Replays with the
// same downstream identity are safe; a different identity is a conflict.
func (s *Service) MarkApprovalStarted(ctx context.Context, tenantID string, requestID uint64, downstreamRequestID string) error {
	tenantID, downstreamRequestID = strings.TrimSpace(tenantID), strings.TrimSpace(downstreamRequestID)
	if tenantID == "" || requestID == 0 || downstreamRequestID == "" {
		return ErrInvalidCallback
	}
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, err := s.repo.FindForUpdate(tx, tenantID, requestID)
		if err != nil {
			return err
		}
		if value.Status == StatusApproving {
			if value.DownstreamRequestID == downstreamRequestID {
				return nil
			}
			return ErrCallbackConflict
		}
		if value.Status != StatusSubmitted {
			return ErrInvalidTransition
		}
		now := s.clock.Now().UTC()
		if err = s.repo.Update(tx, value, value.Version, map[string]any{
			"status": StatusApproving, "downstream_request_id": downstreamRequestID,
			"updated_by": "portal-report-worker", "updated_at": now,
		}); err != nil {
			return err
		}
		return s.repo.CreateStatusEvent(tx, statusEvent(value, "APPROVAL_STARTED", value.Version+1, value.Status, StatusApproving, "SYSTEM", "portal-report-worker", "DELIVERY", downstreamRequestID, "", requestctx.ID(tx), now))
	})
}

func (s *Service) ApplyCallback(ctx context.Context, cb Callback) error {
	cb.TenantID = strings.TrimSpace(cb.TenantID)
	cb.ProjectID = strings.TrimSpace(cb.ProjectID)
	cb.DownstreamRequestID = strings.TrimSpace(cb.DownstreamRequestID)
	cb.IdempotencyKey = strings.TrimSpace(cb.IdempotencyKey)
	cb.ApprovalResult = strings.TrimSpace(cb.ApprovalResult)
	cb.ObjectRef = strings.TrimSpace(cb.ObjectRef)
	cb.FileName = strings.TrimSpace(cb.FileName)
	cb.MIME = strings.TrimSpace(cb.MIME)
	cb.FileHash = strings.ToLower(strings.TrimSpace(cb.FileHash))
	if !validBoundedText(cb.TenantID, maxTenantIDBytes) || cb.RequestID == 0 || cb.CustomerID == 0 ||
		!validBoundedText(cb.ProjectID, maxProjectIDBytes) || cb.Version == 0 ||
		!validBoundedText(cb.DownstreamRequestID, maxDownstreamRequestIDBytes) || !validBoundedText(cb.IdempotencyKey, maxIdempotencyKeyBytes) ||
		len([]byte(cb.ApprovalResult)) > maxApprovalResultBytes || !validOptionalNarrative(cb.ApprovalResult) {
		return ErrInvalidCallback
	}
	if cb.Status != StatusApprovedProcessing && cb.Status != StatusRejected && cb.Status != StatusIssued && cb.Status != StatusProcessingFailed {
		return ErrInvalidCallback
	}
	if (cb.Status == StatusApprovedProcessing || cb.Status == StatusRejected) && cb.ApprovalResult == "" {
		return ErrInvalidCallback
	}
	callbackHash := hashCallback(cb)
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, err := s.repo.FindForUpdate(tx, cb.TenantID, cb.RequestID)
		if err != nil {
			return err
		}
		if value.CustomerID != cb.CustomerID || value.ProjectID != cb.ProjectID || value.DownstreamRequestID == "" || value.DownstreamRequestID != cb.DownstreamRequestID {
			return ErrInvalidCallback
		}
		// The event history retains a digest for every accepted callback key,
		// including callbacks older than last_callback_key. Exact replays are
		// no-ops; reusing any prior key with another payload is always a conflict.
		source := sourceHash("CALLBACK", cb.IdempotencyKey)
		accepted, findErr := s.repo.FindStatusEventBySource(tx, cb.TenantID, value.ID, source)
		if findErr == nil {
			if accepted.PayloadHash == callbackHash {
				return nil
			}
			return ErrCallbackConflict
		}
		if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if value.LastCallbackKey != "" && value.LastCallbackKey == cb.IdempotencyKey && value.LastCallbackHash != callbackHash {
			return ErrCallbackConflict
		}
		if cb.Version < value.LastCallbackVersion {
			return nil
		}
		if cb.Version == value.LastCallbackVersion {
			if value.LastCallbackKey == cb.IdempotencyKey && value.LastCallbackHash == callbackHash {
				return nil
			}
			return ErrCallbackConflict
		}
		if !transitionAllowed(value.Status, cb.Status) {
			return ErrInvalidTransition
		}
		now := s.clock.Now().UTC()
		fields := map[string]any{
			"status": cb.Status, "approval_result": cb.ApprovalResult,
			"last_callback_version": cb.Version, "last_callback_key": cb.IdempotencyKey,
			"last_callback_hash": callbackHash, "updated_by": "project-service", "updated_at": now,
		}
		if cb.Status == StatusApprovedProcessing {
			fields["approved_at"] = &now
		}
		if cb.Status != StatusIssued && (cb.ObjectRef != "" || cb.FileName != "" || cb.MIME != "" || cb.FileHash != "" || cb.Size != 0) {
			return ErrInvalidCallback
		}
		if cb.Status == StatusIssued {
			if !validFileDescriptor(cb) {
				return ErrInvalidCallback
			}
			if s.ingest == nil {
				return errors.New("report ingest queue protection is not configured")
			}
			descriptor := FileDescriptor{ObjectRef: cb.ObjectRef, FileName: cb.FileName, MIME: cb.MIME, FileHash: cb.FileHash, Size: cb.Size}
			raw, marshalErr := json.Marshal(descriptor)
			if marshalErr != nil {
				return marshalErr
			}
			ciphertext, protectErr := s.ingest.Encrypt(tx, raw)
			if protectErr != nil || len(ciphertext) == 0 || len(ciphertext) > 2048 {
				if protectErr != nil {
					return protectErr
				}
				return ErrInvalidCallback
			}
			eventID := sourceHash("INGEST", cb.IdempotencyKey)
			if err = s.repo.CreateIngestJob(tx, &IngestJob{EventID: eventID, TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID, DescriptorCipher: ciphertext, DescriptorHash: descriptorHash(descriptor), Status: IngestPending, CreatedAt: now}); err != nil {
				return err
			}
			fields["status"] = StatusIngestPending
		}
		if err = s.repo.Update(tx, value, value.Version, fields); err != nil {
			return err
		}
		toStatus := cb.Status
		if cb.Status == StatusIssued {
			toStatus = StatusIngestPending
		}
		if err = s.repo.CreateStatusEvent(tx, statusEvent(value, callbackEventType(toStatus), value.Version+1, value.Status, toStatus, "MACHINE", "project-service", "CALLBACK", cb.IdempotencyKey, callbackHash, requestctx.ID(tx), now)); err != nil {
			return err
		}
		return nil
	})
}

// CompleteIngest atomically publishes only evidence returned by the trusted
// ingestor. Object retrieval, scanning and encryption happen before this
// transaction and therefore never extend callback or database lock duration.
func (s *Service) CompleteIngest(ctx context.Context, job IngestJob, descriptor FileDescriptor, ingested IngestResult) error {
	if job.ID == 0 || job.RequestID == 0 || !validBoundedText(job.EventID, 64) || descriptorHash(descriptor) != job.DescriptorHash {
		return ErrInvalidCallback
	}
	ingested.EncryptionKeyRef = strings.TrimSpace(ingested.EncryptionKeyRef)
	ingested.ObjectVersion = strings.TrimSpace(ingested.ObjectVersion)
	ingested.EncryptionAlgorithm = strings.TrimSpace(ingested.EncryptionAlgorithm)
	ingested.ScanStatus = strings.TrimSpace(ingested.ScanStatus)
	ingested.ScanReference = strings.TrimSpace(ingested.ScanReference)
	ingested.WatermarkStatus = strings.TrimSpace(ingested.WatermarkStatus)
	now := s.clock.Now().UTC()
	if len(ingested.ObjectKeyCipher) == 0 || len(ingested.ObjectKeyCipher) > 1024 || !validBoundedText(ingested.ObjectVersion, 256) ||
		!validBoundedText(ingested.EncryptionKeyRef, 255) || ingested.EncryptionAlgorithm != "AES-256-GCM" || ingested.ScanStatus != "CLEAN" ||
		!validBoundedText(ingested.ScanReference, 128) || ingested.ScannedAt.IsZero() || !validBoundedText(ingested.WatermarkStatus, 32) ||
		ingested.ScannedAt.After(now.Add(5*time.Minute)) {
		return errors.New("trusted report ingestor returned invalid security evidence")
	}
	scannedAt := ingested.ScannedAt.UTC()
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		currentJob, err := s.repo.FindIngestJobForUpdate(tx, job.ID)
		if err != nil {
			return err
		}
		if currentJob.EventID != job.EventID || currentJob.TenantID != job.TenantID || currentJob.CustomerID != job.CustomerID || currentJob.RequestID != job.RequestID || currentJob.DescriptorHash != job.DescriptorHash {
			return ErrCallbackConflict
		}
		if currentJob.Status == IngestCompleted {
			return nil
		}
		if currentJob.Status != IngestProcessing || currentJob.LockedBy != job.LockedBy {
			return errors.New("report ingest lease was lost")
		}
		value, err := s.repo.FindForUpdate(tx, job.TenantID, job.RequestID)
		if err != nil {
			return err
		}
		if value.CustomerID != job.CustomerID || value.Status != StatusIngestPending {
			return ErrInvalidTransition
		}
		if err = s.repo.CreateFile(tx, &File{Model: database.Model{TenantID: job.TenantID, CreatedBy: "portal-file-ingestor", UpdatedBy: "portal-file-ingestor", CreatedAt: now, UpdatedAt: now, Version: 1}, RequestID: value.ID, ObjectKeyCipher: ingested.ObjectKeyCipher, ObjectVersion: ingested.ObjectVersion, FileName: descriptor.FileName, MIME: descriptor.MIME, Size: descriptor.Size, FileHash: descriptor.FileHash, EncryptionKeyRef: ingested.EncryptionKeyRef, EncryptionAlgorithm: ingested.EncryptionAlgorithm, ScanStatus: ingested.ScanStatus, ScanReference: ingested.ScanReference, ScannedAt: &scannedAt, WatermarkStatus: ingested.WatermarkStatus}); err != nil {
			return err
		}
		if err = s.repo.Update(tx, value, value.Version, map[string]any{"status": StatusIssued, "issued_at": &now, "updated_by": "portal-file-ingestor", "updated_at": now}); err != nil {
			return err
		}
		if err = s.repo.CreateStatusEvent(tx, statusEvent(value, "REPORT_ISSUED", value.Version+1, StatusIngestPending, StatusIssued, "SYSTEM", "portal-file-ingestor", "INGEST", job.EventID, job.DescriptorHash, requestctx.ID(tx), now)); err != nil {
			return err
		}
		if err = s.repo.CreateNotification(tx, &Notification{TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID, AccountID: value.AccountID, Kind: NotificationKindIssued, Status: NotificationUnread, CreatedAt: now}); err != nil {
			return err
		}
		return s.repo.UpdateIngestJob(tx, currentJob, map[string]any{"status": IngestCompleted, "completed_at": &now, "locked_by": "", "locked_until": nil, "next_retry_at": nil, "last_error_summary": ""})
	})
}

// MarkIngestDeadLetter makes terminal operational failure visible on the
// aggregate without publishing a file or notification. It is exact-replay
// safe and records only the stable job identity, never provider error text.
func (s *Service) MarkIngestDeadLetter(ctx context.Context, job IngestJob) error {
	if job.ID == 0 || job.RequestID == 0 || !validBoundedText(job.EventID, 64) {
		return ErrInvalidCallback
	}
	now := s.clock.Now().UTC()
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		currentJob, err := s.repo.FindIngestJobForUpdate(tx, job.ID)
		if err != nil {
			return err
		}
		if currentJob.EventID != job.EventID || currentJob.TenantID != job.TenantID || currentJob.CustomerID != job.CustomerID || currentJob.RequestID != job.RequestID || currentJob.DescriptorHash != job.DescriptorHash {
			return ErrCallbackConflict
		}
		if currentJob.Status == IngestDeadLetter {
			return nil
		}
		if currentJob.Status != IngestProcessing || currentJob.LockedBy != job.LockedBy {
			return errors.New("report ingest lease was lost")
		}
		value, err := s.repo.FindForUpdate(tx, job.TenantID, job.RequestID)
		if err != nil {
			return err
		}
		if value.CustomerID != job.CustomerID || value.Status != StatusIngestPending {
			return ErrInvalidTransition
		}
		if err = s.repo.Update(tx, value, value.Version, map[string]any{"status": StatusProcessingFailed, "updated_by": "portal-file-ingestor", "updated_at": now}); err != nil {
			return err
		}
		if err = s.repo.CreateStatusEvent(tx, statusEvent(value, "PROCESSING_FAILED", value.Version+1, StatusIngestPending, StatusProcessingFailed, "SYSTEM", "portal-file-ingestor", "INGEST_DEAD_LETTER", job.EventID, job.DescriptorHash, requestctx.ID(tx), now)); err != nil {
			return err
		}
		return s.repo.UpdateIngestJob(tx, currentJob, map[string]any{"status": IngestDeadLetter, "retry_count": currentJob.RetryCount + 1, "locked_by": "", "locked_until": nil, "next_retry_at": nil})
	})
}

func statusEvent(value *Request, eventType string, sequence uint64, from, to Status, actorType, actorID, sourceType, sourceKey, payloadHash, traceID string, occurredAt time.Time) *StatusEvent {
	return &StatusEvent{
		TenantID: value.TenantID, CustomerID: value.CustomerID, RequestID: value.ID,
		EventType: eventType, Sequence: sequence, FromStatus: from, ToStatus: to, ActorType: actorType,
		ActorID: strings.TrimSpace(actorID), SourceKeyHash: sourceHash(sourceType, sourceKey), PayloadHash: payloadHash,
		RequestTrace: strings.TrimSpace(traceID), OccurredAt: occurredAt,
	}
}

func callbackEventType(status Status) string {
	switch status {
	case StatusApprovedProcessing:
		return "APPROVAL_APPROVED"
	case StatusRejected:
		return "APPROVAL_REJECTED"
	case StatusIssued:
		return "REPORT_ISSUED"
	case StatusIngestPending:
		return "REPORT_INGEST_QUEUED"
	case StatusProcessingFailed:
		return "PROCESSING_FAILED"
	default:
		return "STATUS_CHANGED"
	}
}

func descriptorHash(value FileDescriptor) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sourceHash(sourceType, sourceKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sourceType) + "\x00" + strings.TrimSpace(sourceKey)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func transitionAllowed(from, to Status) bool {
	switch from {
	case StatusSubmitted:
		return to == StatusApproving
	case StatusApproving:
		return to == StatusApprovedProcessing || to == StatusRejected
	case StatusApprovedProcessing:
		return to == StatusIssued || to == StatusProcessingFailed
	case StatusProcessingFailed:
		return to == StatusApprovedProcessing || to == StatusIssued
	default:
		return false
	}
}
func allowedMIME(value string) bool { return value == "application/pdf" }

func validFileDescriptor(cb Callback) bool {
	objectRef := strings.TrimSpace(cb.ObjectRef)
	if objectRef == "" || len(objectRef) > 512 || strings.Contains(objectRef, "://") || strings.HasPrefix(objectRef, "/") || strings.Contains(objectRef, "\\") {
		return false
	}
	for _, part := range strings.Split(objectRef, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
				return false
			}
		}
	}
	fileName := strings.TrimSpace(cb.FileName)
	if fileName == "" || len([]byte(fileName)) > 255 || filepath.Base(fileName) != fileName || strings.Contains(fileName, "\\") || !strings.HasSuffix(strings.ToLower(fileName), ".pdf") {
		return false
	}
	for _, r := range fileName {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return allowedMIME(cb.MIME) && cb.Size > 0 && cb.Size <= maxReportFileSize && sha256HexPattern.MatchString(cb.FileHash)
}

func hashCallback(cb Callback) string {
	raw, _ := json.Marshal([]any{
		cb.TenantID, cb.RequestID, cb.CustomerID, cb.ProjectID, cb.Version, cb.Status,
		cb.DownstreamRequestID, cb.ApprovalResult, cb.ObjectRef, cb.FileName,
		cb.MIME, strings.ToLower(cb.FileHash), cb.Size,
	})
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func requestHash(cmd CreateCommand) string {
	raw, _ := json.Marshal([]string{strings.TrimSpace(cmd.ProjectID), strings.TrimSpace(cmd.ReportType), strings.TrimSpace(cmd.Reason), strings.TrimSpace(strings.ToLower(cmd.ReceiveEmail))})
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sameCreateBinding(value *Request, actor Actor, cmd CreateCommand) bool {
	return value != nil && value.TenantID == actor.TenantID && value.CustomerID == actor.CustomerID &&
		value.AccountID == actor.AccountID && value.ProjectID == cmd.ProjectID
}

func validEmail(value string) bool {
	if !validBoundedText(value, maxReceiveEmailBytes) || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && strings.EqualFold(parsed.Address, value)
}

func validBoundedText(value string, maxBytes int) bool {
	return value != "" && len([]byte(value)) <= maxBytes && !containsControl(value)
}

func validNarrative(value string, maxBytes int) bool {
	return value != "" && len([]byte(value)) <= maxBytes && validOptionalNarrative(value)
}

func validOptionalNarrative(value string) bool {
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
