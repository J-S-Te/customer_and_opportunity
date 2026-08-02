package report

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

const (
	defaultGrantTTL              = 72 * time.Hour
	minimumTokenLen              = 32
	maximumTokenLen              = 256
	maxConcurrentDownloadBuffers = 2
)

var (
	ErrReportNotIssued     = apperror.New(http.StatusConflict, "PORTAL_REPORT_NOT_ISSUED", "report is not issued")
	ErrFileUnavailable     = apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_FILE_UNAVAILABLE", "trusted report file is unavailable")
	ErrGrantNotFound       = apperror.New(http.StatusNotFound, "PORTAL_REPORT_GRANT_NOT_FOUND", "download authorization not found")
	ErrGrantExpired        = apperror.New(http.StatusGone, "PORTAL_REPORT_LINK_EXPIRED", "download authorization expired")
	ErrGrantFrozen         = apperror.New(http.StatusLocked, "PORTAL_REPORT_GRANT_FROZEN", "download authorization is frozen")
	ErrGrantRevoked        = apperror.New(http.StatusGone, "PORTAL_REPORT_GRANT_REVOKED", "download authorization was revoked")
	ErrDownloadUnavailable = apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_DOWNLOAD_UNAVAILABLE", "secure report download is unavailable")
	ErrDownloadIntegrity   = apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_DOWNLOAD_INTEGRITY_FAILED", "report content integrity check failed")
	ErrIssueReplay         = apperror.New(http.StatusConflict, "PORTAL_REPORT_GRANT_REPLAY", "download authorization response cannot be replayed; request a new authorization")
	ErrStreamIncomplete    = apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_STREAM_INCOMPLETE", "report stream did not complete")
	ErrDownloadAudit       = apperror.New(http.StatusServiceUnavailable, "PORTAL_REPORT_AUDIT_UNAVAILABLE", "report download audit is unavailable")
)

type TokenGenerator interface{ NewToken() (string, error) }

type CryptoTokenGenerator struct{}

func (CryptoTokenGenerator) NewToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type GrantResult struct {
	GrantID       string
	Status        GrantStatus
	ExpiresAt     time.Time
	DownloadToken string
}

type DownloadMetadata struct {
	IPHash     string
	DeviceHash string
	// TrustedLocationCode is populated only from a mutually authenticated
	// gateway/GeoIP contract. Direct browser headers are never promoted into it.
	TrustedLocationCode string
}

type GrantCommand struct {
	IdempotencyKey string
	Metadata       DownloadMetadata
}

type DownloadContent struct {
	Reader   io.ReadCloser
	FileName string
	MIME     string
	Size     int64
	FileHash string
	complete func(context.Context, bool, string) error
}

type slotReadCloser struct {
	io.ReadCloser
	release func()
}

func (r *slotReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}

type PreparedDownload struct {
	Reader   io.ReadCloser
	FileName string
	MIME     string
	Size     int64
	FileHash string
}

func (c *DownloadContent) Complete(ctx context.Context, success bool, reason string) error {
	if c == nil || c.complete == nil {
		return nil
	}
	return c.complete(ctx, success, reason)
}

// DownloadRiskPolicy is the explicit trust boundary for gateway/GeoIP-based
// anomaly rules. The default policy only permits a configured trusted policy;
// bootstrap leaves it nil and therefore does not invent location assertions.
type DownloadRiskPolicy interface {
	Evaluate(context.Context, Actor, *Request, *Grant, DownloadMetadata) (freeze bool, reason string, err error)
}

type WatermarkContext struct {
	TenantID, AccountID, TrackingCode string
	CustomerID, RequestID             uint64
	DownloadedAt                      time.Time
}

// DownloadWatermarker returns a newly rendered PDF containing the customer
// name, masked account, request number/time and an unpredictable tracking code.
// Identity and CJK font/template resolution belong to the implementation; the
// download service never invents those values from numeric IDs.
type DownloadWatermarker interface {
	Apply(context.Context, []byte, WatermarkContext) ([]byte, error)
}

// FileReader must return plaintext whose size/hash/MIME have already been
// verified against File. Implementations own object retrieval and envelope
// decryption; returning an unverified object stream violates this contract.
type FileReader interface {
	OpenVerified(context.Context, *File) (PreparedDownload, error)
}

type availabilityReporter interface {
	Available() bool
}

type DownloadRepository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	Find(context.Context, string, uint64, uint64) (*Request, error)
	FindForUpdate(context.Context, string, uint64) (*Request, error)
	FindFile(context.Context, string, uint64) (*File, error)
	RevokeActiveGrants(context.Context, string, uint64, uint64, string, time.Time) error
	CreateGrant(context.Context, *Grant) error
	FindGrantByIssueKeyForUpdate(context.Context, string, uint64, uint64, string, string) (*Grant, error)
	FindGrantForUpdate(context.Context, string, uint64, uint64, string, string) (*Grant, error)
	UpdateGrant(context.Context, *Grant, map[string]any) error
	CreateDownloadEvent(context.Context, *DownloadEvent) error
	CreateDownloadEventOnce(context.Context, *DownloadEvent) error
	CreateRiskAlert(context.Context, *RiskAlert) error
	ListRiskAlerts(context.Context, Actor, bool, int, int) (pagination.Page[RiskAlertView], error)
	ListRiskAlertsForReview(context.Context, string, string, int, int) (pagination.Page[RiskAlertView], error)
	FindRiskAlertForUpdate(context.Context, string, string) (*RiskAlert, error)
	FindRiskAlertView(context.Context, string, string) (*RiskAlertView, error)
	UpdateRiskAlert(context.Context, *RiskAlert, map[string]any) error
	CreateRiskReviewEvent(context.Context, *RiskReviewEvent) error
	FindRiskReviewEvent(context.Context, string, string, string) (*RiskReviewEvent, error)
	FindGrantByIDForUpdate(context.Context, string, uint64) (*Grant, error)
	FindActiveGrantForUpdate(context.Context, string, uint64, uint64, string) (*Grant, error)
}

type DownloadService struct {
	repo                      DownloadRepository
	files                     FileReader
	risk                      DownloadRiskPolicy
	clock                     Clock
	ids                       IDGenerator
	tokens                    TokenGenerator
	ttl                       time.Duration
	bufferSlots               chan struct{}
	watermarks                DownloadWatermarker
	requireProductionSecurity bool
}

// RequireProductionSecurity makes trusted risk evaluation and per-download
// watermarking mandatory. Portal bootstrap enables this even while adapters
// are unavailable so partial production configuration cannot silently bypass
// either control.
func (s *DownloadService) RequireProductionSecurity(watermarks DownloadWatermarker) *DownloadService {
	s.requireProductionSecurity = true
	s.watermarks = watermarks
	return s
}

func NewDownloadService(repo DownloadRepository, files FileReader, risk DownloadRiskPolicy, clock Clock, ids IDGenerator, tokens TokenGenerator, ttl time.Duration) *DownloadService {
	if ttl <= 0 || ttl > defaultGrantTTL {
		ttl = defaultGrantTTL
	}
	return &DownloadService{repo: repo, files: files, risk: risk, clock: clock, ids: ids, tokens: tokens, ttl: ttl, bufferSlots: make(chan struct{}, maxConcurrentDownloadBuffers)}
}

// RuntimeAvailable reports whether the dependencies required by the secure
// download path are actually injected. It is intentionally local-only: it
// never probes a remote Provider and never exposes configuration values.
func (s *DownloadService) RuntimeAvailable() bool {
	if s == nil || s.files == nil {
		return false
	}
	if reporter, ok := s.files.(availabilityReporter); ok && !reporter.Available() {
		return false
	}
	if s.requireProductionSecurity && (s.risk == nil || s.watermarks == nil) {
		return false
	}
	return true
}

func (s *DownloadService) CreateGrant(ctx context.Context, actor Actor, requestID uint64, command GrantCommand) (*GrantResult, error) {
	actor.TenantID = strings.TrimSpace(actor.TenantID)
	actor.AccountID = strings.TrimSpace(actor.AccountID)
	idempotencyKey, metadata := strings.TrimSpace(command.IdempotencyKey), command.Metadata
	if !validActor(actor) || requestID == 0 || !validBoundedText(idempotencyKey, maxIdempotencyKeyBytes) {
		return nil, ErrInvalidRequest
	}
	token, err := s.tokens.NewToken()
	if err != nil || len(token) < minimumTokenLen || len(token) > maximumTokenLen {
		if err != nil {
			return nil, err
		}
		return nil, ErrDownloadUnavailable
	}
	now := s.clock.Now().UTC()
	publicID := strings.TrimSpace(s.ids.NewID())
	if !validBoundedText(publicID, 64) {
		return nil, ErrDownloadUnavailable
	}
	grant := &Grant{
		ActorModel: ActorModel{TenantID: actor.TenantID, CreatedBy: actor.AccountID, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1},
		PublicID:   publicID, CustomerID: actor.CustomerID, RequestID: requestID, AccountID: actor.AccountID,
		TokenHash: tokenHash(token), IssueKeyHash: sourceHash("GRANT", idempotencyKey), Status: GrantActive,
		ExpiresAt: now.Add(s.ttl),
	}
	active := "ACTIVE"
	grant.ActiveSlot = &active
	err = s.repo.WithTransaction(ctx, func(tx context.Context) error {
		request, findErr := s.repo.FindForUpdate(tx, actor.TenantID, requestID)
		if findErr != nil {
			return findErr
		}
		if request.CustomerID != actor.CustomerID {
			return ErrNotFound
		}
		if request.Status != StatusIssued {
			return ErrReportNotIssued
		}
		if _, fileErr := s.repo.FindFile(tx, actor.TenantID, request.ID); fileErr != nil {
			return fileErr
		}
		if _, replayErr := s.repo.FindGrantByIssueKeyForUpdate(tx, actor.TenantID, actor.CustomerID, request.ID, actor.AccountID, grant.IssueKeyHash); replayErr == nil {
			return ErrIssueReplay
		} else if !errors.Is(replayErr, ErrGrantNotFound) {
			return replayErr
		}
		if revokeErr := s.repo.RevokeActiveGrants(tx, actor.TenantID, actor.CustomerID, request.ID, actor.AccountID, now); revokeErr != nil {
			return revokeErr
		}
		if createErr := s.repo.CreateGrant(tx, grant); createErr != nil {
			return createErr
		}
		return s.repo.CreateDownloadEvent(tx, downloadEvent(actor, requestID, &grant.ID, "GRANT_ISSUED", "SUCCESS", "", metadata, sourceHash("GRANT", idempotencyKey), requestctx.ID(tx), now))
	})
	if err != nil {
		// Losing a same-scope ACTIVE-slot race never returns another request's
		// credential and never persists plaintext. The caller must request a new
		// authorization with a new key because an old plaintext token cannot be
		// reconstructed safely.
		if strings.Contains(strings.ToLower(err.Error()), "uq_portal_report_grant_active") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, ErrIssueReplay
		}
		return nil, err
	}
	return &GrantResult{GrantID: grant.PublicID, Status: GrantActive, ExpiresAt: grant.ExpiresAt, DownloadToken: token}, nil
}

// AuthorizeDownload consumes no token and opens no object until every scoped
// check passes. Dependency failures are audited but do not increase counters.
func (s *DownloadService) AuthorizeDownload(ctx context.Context, actor Actor, requestID uint64, plaintextToken string, metadata DownloadMetadata) (*DownloadContent, error) {
	actor.TenantID = strings.TrimSpace(actor.TenantID)
	actor.AccountID = strings.TrimSpace(actor.AccountID)
	if !validActor(actor) || requestID == 0 {
		return nil, ErrGrantNotFound
	}
	var value *Grant
	var file *File
	var decisionErr error
	now := s.clock.Now().UTC()
	if len(plaintextToken) < minimumTokenLen || len(plaintextToken) > maximumTokenLen {
		if err := s.auditInvalidToken(ctx, actor, requestID, metadata, now); err != nil {
			// A missing or foreign parent cannot accept a referentially valid audit
			// row. Keep it indistinguishable from every other invalid credential;
			// only a real audit dependency/write failure is surfaced as 503.
			if errors.Is(err, ErrNotFound) {
				return nil, ErrGrantNotFound
			}
			return nil, apperror.Wrap(err, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrGrantNotFound
	}
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		request, findErr := s.repo.Find(tx, actor.TenantID, actor.CustomerID, requestID)
		if findErr != nil {
			return findErr
		}
		grant, grantErr := s.repo.FindGrantForUpdate(tx, actor.TenantID, actor.CustomerID, requestID, actor.AccountID, tokenHash(plaintextToken))
		if grantErr != nil {
			if errors.Is(grantErr, ErrGrantNotFound) {
				decisionErr = ErrGrantNotFound
				event := downloadEvent(actor, requestID, nil, "DOWNLOAD_DENIED", "DENIED", "PORTAL_REPORT_GRANT_NOT_FOUND", metadata, "", requestctx.ID(tx), now)
				bucket := invalidAttemptDedupeKey(actor, requestID, now)
				event.DedupeKey = &bucket
				return s.repo.CreateDownloadEventOnce(tx, event)
			}
			return grantErr
		}
		value = grant
		if !sameGrantScope(grant, actor, requestID) {
			return ErrGrantNotFound
		}
		if subtle.ConstantTimeCompare([]byte(grant.TokenHash), []byte(tokenHash(plaintextToken))) != 1 {
			return ErrGrantNotFound
		}
		if !now.Before(grant.ExpiresAt) {
			if grant.Status == GrantActive {
				if updateErr := s.repo.UpdateGrant(tx, grant, map[string]any{"status": GrantExpired, "active_slot": nil, "updated_by": actor.AccountID, "updated_at": now}); updateErr != nil {
					return updateErr
				}
			}
			decisionErr = ErrGrantExpired
			return s.auditDenied(tx, actor, requestID, grant, "EXPIRED", "PORTAL_REPORT_LINK_EXPIRED", metadata, now)
		}
		switch grant.Status {
		case GrantFrozen:
			decisionErr = ErrGrantFrozen
			return s.auditDenied(tx, actor, requestID, grant, "FROZEN", "PORTAL_REPORT_GRANT_FROZEN", metadata, now)
		case GrantRevoked:
			decisionErr = ErrGrantRevoked
			return s.auditDenied(tx, actor, requestID, grant, "REVOKED", "PORTAL_REPORT_GRANT_REVOKED", metadata, now)
		case GrantExpired:
			decisionErr = ErrGrantExpired
			return s.auditDenied(tx, actor, requestID, grant, "EXPIRED", "PORTAL_REPORT_LINK_EXPIRED", metadata, now)
		case GrantActive:
		default:
			decisionErr = ErrGrantNotFound
			return s.auditDenied(tx, actor, requestID, grant, "DENIED", "PORTAL_REPORT_GRANT_INVALID", metadata, now)
		}
		if s.risk != nil {
			freeze, reason, riskErr := s.risk.Evaluate(tx, actor, request, grant, metadata)
			if riskErr != nil {
				return riskErr
			}
			if freeze {
				publicID := ""
				if s.ids != nil {
					publicID = strings.TrimSpace(s.ids.NewID())
				}
				if !validBoundedText(publicID, 64) {
					publicID = sourceHash("RISK_ALERT", grant.PublicID+"\x00"+strconv.FormatInt(now.UnixNano(), 10))
				}
				riskCode := normalizeRiskCode(reason)
				// Persist the denied event first. A repository transaction rolls the
				// alert and grant mutation back if immutable audit cannot be written.
				if auditErr := s.auditDenied(tx, actor, requestID, grant, "FROZEN", riskCode, metadata, now); auditErr != nil {
					return auditErr
				}
				if updateErr := s.repo.UpdateGrant(tx, grant, map[string]any{"status": GrantFrozen, "active_slot": nil, "risk_state": riskCode, "updated_by": "risk-policy", "updated_at": now}); updateErr != nil {
					return updateErr
				}
				active := "OPEN"
				if alertErr := s.repo.CreateRiskAlert(tx, &RiskAlert{
					PublicID: publicID, TenantID: actor.TenantID, CustomerID: actor.CustomerID,
					RequestID: requestID, GrantID: grant.ID, AccountID: actor.AccountID,
					RiskCode: riskCode, Status: RiskAlertOpen, ActiveSlot: &active,
					DetectedAt: now, RequestTrace: strings.TrimSpace(requestctx.ID(tx)), Version: 1,
				}); alertErr != nil {
					return alertErr
				}
				decisionErr = ErrGrantFrozen
				return nil
			}
		}
		resolved, fileErr := s.repo.FindFile(tx, actor.TenantID, request.ID)
		if fileErr != nil {
			return fileErr
		}
		file = resolved
		return nil
	})
	if err != nil {
		if decisionErr != nil || value != nil {
			return nil, apperror.Wrap(err, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, err
	}
	if decisionErr != nil {
		return nil, decisionErr
	}
	if s.requireProductionSecurity && s.risk == nil {
		if auditErr := s.recordAttempt(ctx, actor, requestID, value, "RISK_POLICY_UNAVAILABLE", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadUnavailable
	}
	if file == nil || file.Size <= 0 || file.Size > maxReportFileSize || !validFileSecurityEvidence(file) {
		if auditErr := s.recordAttempt(ctx, actor, requestID, value, "INTEGRITY_FAILED", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadIntegrity
	}
	if s.files == nil {
		if auditErr := s.recordAttempt(ctx, actor, requestID, value, "DEPENDENCY_UNAVAILABLE", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadUnavailable
	}
	select {
	case s.bufferSlots <- struct{}{}:
	case <-ctx.Done():
		auditCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if auditErr := s.recordAttempt(auditCtx, actor, requestID, value, "DOWNLOAD_CAPACITY_CANCELLED", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadUnavailable
	}
	prepared, err := s.files.OpenVerified(ctx, file)
	if err != nil {
		releaseBufferSlot(s.bufferSlots)
		auditCtx, cancel := failureAuditContext(ctx)
		defer cancel()
		if auditErr := s.recordAttempt(auditCtx, actor, requestID, value, "DEPENDENCY_UNAVAILABLE", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadUnavailable
	}
	if prepared.Reader == nil || prepared.MIME != file.MIME || prepared.Size != file.Size || !strings.EqualFold(prepared.FileHash, file.FileHash) || prepared.FileName != file.FileName {
		if prepared.Reader != nil {
			_ = prepared.Reader.Close()
		}
		releaseBufferSlot(s.bufferSlots)
		auditCtx, cancel := failureAuditContext(ctx)
		defer cancel()
		if auditErr := s.recordAttempt(auditCtx, actor, requestID, value, "INTEGRITY_FAILED", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadIntegrity
	}
	// OpenVerified is a trust-boundary contract, but a stream can still be
	// truncated or swapped after its metadata is returned. Buffer at most the
	// product's 50 MiB file limit and verify before the handler commits a 200.
	// This intentionally trades bounded memory for correct HTTP semantics and
	// avoids writing decrypted reports to temporary disk. A small process-local
	// semaphore caps concurrent buffers; waiting is cancellable with the request.
	raw, readErr := io.ReadAll(io.LimitReader(prepared.Reader, file.Size+1))
	closeErr := prepared.Reader.Close()
	actualHash := sha256.Sum256(raw)
	if readErr != nil || closeErr != nil || int64(len(raw)) != file.Size || !strings.EqualFold(hex.EncodeToString(actualHash[:]), file.FileHash) {
		releaseBufferSlot(s.bufferSlots)
		auditCtx, cancel := failureAuditContext(ctx)
		defer cancel()
		if auditErr := s.recordAttempt(auditCtx, actor, requestID, value, "INTEGRITY_FAILED", metadata); auditErr != nil {
			return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
		}
		return nil, ErrDownloadIntegrity
	}
	served := raw
	trackingDigest := ""
	if s.requireProductionSecurity {
		trackingCode := ""
		if s.ids != nil {
			trackingCode = strings.TrimSpace(s.ids.NewID())
		}
		if s.watermarks == nil || !validBoundedText(trackingCode, 64) {
			releaseBufferSlot(s.bufferSlots)
			if auditErr := s.recordAttempt(ctx, actor, requestID, value, "WATERMARK_UNAVAILABLE", metadata); auditErr != nil {
				return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
			}
			return nil, ErrDownloadUnavailable
		}
		served, err = s.watermarks.Apply(ctx, raw, WatermarkContext{TenantID: actor.TenantID, CustomerID: actor.CustomerID, AccountID: actor.AccountID, RequestID: requestID, DownloadedAt: now, TrackingCode: trackingCode})
		if err != nil || len(served) == 0 || len(served) > maxReportFileSize || !bytes.HasPrefix(served, []byte("%PDF-")) {
			releaseBufferSlot(s.bufferSlots)
			if auditErr := s.recordAttempt(ctx, actor, requestID, value, "WATERMARK_FAILED", metadata); auditErr != nil {
				return nil, apperror.Wrap(auditErr, ErrDownloadAudit.HTTPStatus, ErrDownloadAudit.Code, ErrDownloadAudit.Message)
			}
			return nil, ErrDownloadUnavailable
		}
		trackingDigest = watermarkTrackingDigest(actor, requestID, trackingCode)
	}
	servedHash := sha256.Sum256(served)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { releaseBufferSlot(s.bufferSlots) }) }
	return &DownloadContent{Reader: &slotReadCloser{ReadCloser: io.NopCloser(bytes.NewReader(served)), release: release}, FileName: file.FileName, MIME: file.MIME, Size: int64(len(served)), FileHash: hex.EncodeToString(servedHash[:]), complete: func(doneCtx context.Context, success bool, reason string) error {
		defer release()
		result := "FAILED"
		event := "DOWNLOAD_FAILED"
		if success {
			result, event = "SUCCESS", "DOWNLOAD_SUCCEEDED"
		}
		return s.completeAttempt(doneCtx, actor, requestID, value, event, result, reason, metadata, trackingDigest, success)
	}}, nil
}

func normalizeRiskCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return "TRUSTED_RISK_RULE"
	}
	for _, character := range value {
		if !(character == '_' || character == '-' || character == ':' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return "TRUSTED_RISK_RULE"
		}
	}
	return value
}

func validFileSecurityEvidence(file *File) bool {
	if file == nil {
		return false
	}
	fileHash := strings.TrimSpace(file.FileHash)
	return validBoundedText(strings.TrimSpace(file.ObjectVersion), 256) &&
		file.EncryptionAlgorithm == "AES-256-GCM" && validBoundedText(strings.TrimSpace(file.EncryptionKeyRef), 255) &&
		file.ScanStatus == "CLEAN" && validBoundedText(strings.TrimSpace(file.ScanReference), 128) && file.ScannedAt != nil && !file.ScannedAt.IsZero() &&
		fileHash == strings.ToLower(fileHash) && sha256HexPattern.MatchString(fileHash)
}

func (s *DownloadService) auditInvalidToken(ctx context.Context, actor Actor, requestID uint64, metadata DownloadMetadata, now time.Time) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		if _, err := s.repo.Find(tx, actor.TenantID, actor.CustomerID, requestID); err != nil {
			return err
		}
		event := downloadEvent(actor, requestID, nil, "DOWNLOAD_DENIED", "DENIED", "PORTAL_REPORT_GRANT_NOT_FOUND", metadata, "", requestctx.ID(tx), now)
		bucket := invalidAttemptDedupeKey(actor, requestID, now)
		event.DedupeKey = &bucket
		return s.repo.CreateDownloadEventOnce(tx, event)
	})
}

func releaseBufferSlot(slots chan struct{}) { <-slots }

func failureAuditContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), 3*time.Second)
}

func (s *DownloadService) auditDenied(ctx context.Context, actor Actor, requestID uint64, grant *Grant, result, reason string, metadata DownloadMetadata, now time.Time) error {
	return s.repo.CreateDownloadEvent(ctx, downloadEvent(actor, requestID, &grant.ID, "DOWNLOAD_DENIED", result, reason, metadata, "", requestctx.ID(ctx), now))
}

func (s *DownloadService) recordAttempt(ctx context.Context, actor Actor, requestID uint64, grant *Grant, reason string, metadata DownloadMetadata) error {
	return s.repo.WithTransaction(ctx, func(tx context.Context) error {
		return s.repo.CreateDownloadEvent(tx, downloadEvent(actor, requestID, &grant.ID, "DOWNLOAD_FAILED", reason, reason, metadata, "", requestctx.ID(tx), s.clock.Now().UTC()))
	})
}

func (s *DownloadService) completeAttempt(ctx context.Context, actor Actor, requestID uint64, grant *Grant, eventType, result, reason string, metadata DownloadMetadata, trackingDigest string, success bool) error {
	now := s.clock.Now().UTC()
	var decisionErr error
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		locked, err := s.repo.FindGrantForUpdate(tx, actor.TenantID, actor.CustomerID, requestID, actor.AccountID, grant.TokenHash)
		if err != nil || !sameGrantScope(locked, actor, requestID) {
			if err != nil {
				return err
			}
			return ErrGrantNotFound
		}
		if success {
			if locked.Status != GrantActive || !now.Before(locked.ExpiresAt) {
				decisionErr = ErrGrantRevoked
				return s.repo.CreateDownloadEvent(tx, downloadEvent(actor, requestID, &locked.ID, "DOWNLOAD_FAILED", "REVOKED_DURING_STREAM", "PORTAL_REPORT_GRANT_REVOKED", metadata, "", requestctx.ID(tx), now))
			}
			if err = s.repo.UpdateGrant(tx, locked, map[string]any{"download_count": locked.DownloadCount + 1, "last_download_at": &now, "updated_by": actor.AccountID, "updated_at": now}); err != nil {
				return err
			}
		}
		if !success {
			if err = s.repo.CreateDownloadEvent(tx, downloadEvent(actor, requestID, &locked.ID, eventType, result, strings.TrimSpace(reason), metadata, "", requestctx.ID(tx), now)); err != nil {
				return err
			}
			decisionErr = ErrStreamIncomplete
			return nil
		}
		event := downloadEvent(actor, requestID, &locked.ID, eventType, result, strings.TrimSpace(reason), metadata, "", requestctx.ID(tx), now)
		if success {
			event.TrackingDigest = trackingDigest
		}
		return s.repo.CreateDownloadEvent(tx, event)
	})
	if err != nil {
		return err
	}
	return decisionErr
}

// watermarkTrackingDigest binds the opaque code printed in the PDF to the
// tenant/customer/account/report scope without storing the plaintext code.
// Operations can hash a reported code with the same canonical coordinates to
// locate the exact append-only success event.
func watermarkTrackingDigest(actor Actor, requestID uint64, trackingCode string) string {
	value := strings.Join([]string{actor.TenantID, strconv.FormatUint(actor.CustomerID, 10), actor.AccountID, strconv.FormatUint(requestID, 10), trackingCode}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func downloadEvent(actor Actor, requestID uint64, grantID *uint64, eventType, result, reason string, metadata DownloadMetadata, idempotencyHash, trace string, now time.Time) *DownloadEvent {
	return &DownloadEvent{TenantID: actor.TenantID, CustomerID: actor.CustomerID, RequestID: requestID, GrantID: grantID, AccountID: actor.AccountID, EventType: eventType, Result: result, ReasonCode: reason, IPHash: metadata.IPHash, DeviceHash: metadata.DeviceHash, IdempotencyHash: idempotencyHash, RequestTrace: trace, OccurredAt: now}
}

func validActor(actor Actor) bool {
	return strings.TrimSpace(actor.TenantID) != "" && actor.CustomerID != 0 && strings.TrimSpace(actor.AccountID) != ""
}

func sameGrantScope(grant *Grant, actor Actor, requestID uint64) bool {
	return grant != nil && grant.TenantID == actor.TenantID && grant.CustomerID == actor.CustomerID && grant.RequestID == requestID && grant.AccountID == actor.AccountID
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// invalidAttemptDedupeKey caps unauthenticated-token denial rows to one per
// scoped account/report/hour. It intentionally excludes the token, so neither
// plaintext credentials nor token digests become an audit-table oracle.
func invalidAttemptDedupeKey(actor Actor, requestID uint64, now time.Time) string {
	bucket := now.UTC().Truncate(time.Hour).Format(time.RFC3339)
	return sourceHash("INVALID_DOWNLOAD", actor.TenantID+"\x00"+actor.AccountID+"\x00"+strconv.FormatUint(requestID, 10)+"\x00"+bucket)
}

func IsDownloadClientError(err error) bool {
	return errors.Is(err, ErrGrantExpired) || errors.Is(err, ErrGrantFrozen) || errors.Is(err, ErrGrantRevoked) || errors.Is(err, ErrGrantNotFound)
}
