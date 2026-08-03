// Package portalfilingworker 只提交已经在本地锁定的备案快照；部署必须提供实现签名及验签协议的正式安全适配器。
package portalfilingworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/filing"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	contractVersion = "portal.filing.submission-reference.v1"
	maxAttempts     = 7
)

var errLeaseLost = errors.New("Portal filing submission lease was lost")

// SubmissionBundle 与具体备案机构无关。SnapshotJSON 是 Portal 加密保存的不可变规范快照；
// Materials 只包含不可变且扫描结果为 CLEAN 的对象引用和证据，适配器转换正式报文时不得绕过这些约束。
type SubmissionBundle struct {
	EventID        string
	TenantID       string
	FilingID       string
	FilingNo       string
	FormVersion    string
	SnapshotSHA256 string
	SnapshotJSON   []byte
	Materials      []MaterialEvidence
}

type MaterialEvidence struct {
	Code, ObjectKey, ObjectVersion, FileName, MIMEType, SHA256, ScanReference string
	SizeBytes                                                                 uint64
	ScannedAt                                                                 time.Time
}

// Receipt 仅在机构确认接收后返回。Evidence 是已验签的规范回执原文；进入 Store 前会被密文替换，
// 并保留明文 SHA-256 用于完整性核验，数据库不会持久化回执明文。
type Receipt struct {
	ID, Authority  string
	ReceivedAt     time.Time
	Evidence       []byte
	EvidenceCipher []byte
	EvidenceSHA256 string
}

type Provider interface {
	Available() bool
	// Submit 必须把 EventID 与完整报文绑定，并在重试时返回相同的已验证回执；上下文取消必须终止在途 I/O。
	Submit(context.Context, SubmissionBundle) (Receipt, error)
}

type Protector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}

type Worker struct {
	store         Store
	protector     Protector
	provider      Provider
	workerID      string
	pollInterval  time.Duration
	leaseDuration time.Duration
	batchSize     int
	now           func() time.Time
}

type Store interface {
	Activate(context.Context, string, time.Time, int) (int64, error)
	Claim(context.Context, string, time.Time, time.Duration, int) ([]filing.SubmissionOutbox, error)
	LoadBundle(context.Context, Protector, filing.SubmissionOutbox) (SubmissionBundle, error)
	Complete(context.Context, string, filing.SubmissionOutbox, Receipt, time.Time) error
	Fail(context.Context, string, filing.SubmissionOutbox, string, uint32, *time.Time, time.Time) error
}

func NewWorker(db *gorm.DB, protector Protector, provider Provider, workerID string, pollInterval, leaseDuration time.Duration, batchSize int) (*Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if db == nil || protector == nil || provider == nil || !provider.Available() || workerID == "" || len(workerID) > 128 || pollInterval <= 0 || leaseDuration < 10*time.Second || batchSize < 1 || batchSize > 100 {
		return nil, errors.New("Portal filing submission worker requires a configured formal provider and valid scheduling options")
	}
	return NewWorkerWithStore(&GORMStore{db: db}, protector, provider, workerID, pollInterval, leaseDuration, batchSize)
}

func NewWorkerWithStore(store Store, protector Protector, provider Provider, workerID string, pollInterval, leaseDuration time.Duration, batchSize int) (*Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if store == nil || protector == nil || provider == nil || !provider.Available() || workerID == "" || len(workerID) > 128 || pollInterval <= 0 || leaseDuration < 10*time.Second || batchSize < 1 || batchSize > 100 {
		return nil, errors.New("Portal filing submission worker requires a configured formal provider and valid scheduling options")
	}
	return &Worker{store: store, protector: protector, provider: provider, workerID: workerID, pollInterval: pollInterval, leaseDuration: leaseDuration, batchSize: batchSize, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Portal filing submission worker poll failed: %s", safeError(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if _, err := w.store.Activate(ctx, contractVersion, w.now().UTC(), w.batchSize); err != nil {
		return 0, err
	}
	processed := 0
	var joined error
	// 每次只在真正提交前领取一条；若整批先领取再串行发送，后排任务可能在开始远端调用前就失去租约。
	for processed < w.batchSize {
		events, err := w.store.Claim(ctx, w.workerID, w.now().UTC(), w.leaseDuration, 1)
		if err != nil {
			return processed, errors.Join(joined, err)
		}
		if len(events) == 0 {
			break
		}
		processed++
		if dispatchErr := w.dispatch(ctx, events[0]); dispatchErr != nil {
			joined = errors.Join(joined, dispatchErr)
		}
	}
	return processed, joined
}

type GORMStore struct{ db *gorm.DB }

// Activate 只激活仍处于本地锁定状态的备案，并原子推进备案与 Outbox；远端提交期间管理员不能解锁或改写快照。
func (s *GORMStore) Activate(ctx context.Context, version string, now time.Time, limit int) (int64, error) {
	var activated int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []filing.SubmissionOutbox
		if err := tx.Raw(`SELECT o.* FROM portal_filing_submission_outbox o
JOIN portal_filings f ON f.tenant_id=o.tenant_id AND f.id=o.filing_id AND f.deleted_at IS NULL
WHERE o.contract_version=? AND o.status='WAITING_CONTRACT' AND f.status='WAITING_CONTRACT'
	AND o.submission_id=(SELECT s.id FROM portal_filing_submission_snapshots s
		WHERE s.tenant_id=o.tenant_id AND s.filing_id=o.filing_id
		ORDER BY s.sequence DESC LIMIT 1)
ORDER BY o.created_at,o.id LIMIT ? FOR UPDATE SKIP LOCKED`, version, limit).Scan(&events).Error; err != nil {
			return err
		}
		for i := range events {
			if result := tx.Model(&filing.Filing{}).Where("tenant_id=? AND id=? AND status=?", events[i].TenantID, events[i].FilingID, filing.StatusWaitingContract).Updates(map[string]any{"status": filing.StatusSubmitting, "updated_by": "public-security-provider", "updated_at": now, "version": gorm.Expr("version+1")}); result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return errLeaseLost
			}
			if result := tx.Model(&filing.SubmissionOutbox{}).Where("id=? AND status='WAITING_CONTRACT'", events[i].ID).Updates(map[string]any{"status": "PENDING", "next_retry_at": now}); result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return errLeaseLost
			}
			activated++
		}
		return nil
	})
	return activated, err
}

func (s *GORMStore) Claim(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration, batchSize int) ([]filing.SubmissionOutbox, error) {
	claimed := make([]filing.SubmissionOutbox, 0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []filing.SubmissionOutbox
		if err := tx.Raw(`SELECT o.* FROM portal_filing_submission_outbox o
JOIN portal_filings f ON f.tenant_id=o.tenant_id AND f.id=o.filing_id AND f.deleted_at IS NULL
WHERE o.contract_version = ? AND f.status='SUBMITTING' AND
 ((o.status = 'PENDING' AND (o.next_retry_at IS NULL OR o.next_retry_at <= ?))
  OR (o.status = 'PROCESSING' AND o.locked_until < ?))
ORDER BY o.created_at,o.id LIMIT ? FOR UPDATE SKIP LOCKED`, contractVersion, now, now, batchSize).Scan(&events).Error; err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(events))
		for i := range events {
			ids = append(ids, events[i].ID)
		}
		lockedUntil := now.Add(leaseDuration)
		result := tx.Model(&filing.SubmissionOutbox{}).Where("id IN ?", ids).Updates(map[string]any{"status": "PROCESSING", "locked_by": workerID, "locked_until": lockedUntil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(events)) {
			return errLeaseLost
		}
		for i := range events {
			events[i].Status, events[i].LockedBy, events[i].LockedUntil = "PROCESSING", workerID, &lockedUntil
		}
		claimed = events
		return nil
	})
	return claimed, err
}

func (w *Worker) dispatch(ctx context.Context, event filing.SubmissionOutbox) error {
	now := w.now().UTC()
	if event.Status != "PROCESSING" || event.LockedBy != w.workerID || event.LockedUntil == nil || !event.LockedUntil.After(now.Add(time.Second)) {
		return errors.New("Portal filing submission event has no usable lease")
	}
	// 从租约中预留一秒用于失败落账；加载快照、远端调用、回执加密和本地提交共用其余期限。
	workCtx, cancel := context.WithDeadline(ctx, event.LockedUntil.Add(-time.Second))
	defer cancel()
	bundle, err := w.store.LoadBundle(workCtx, w.protector, event)
	if err != nil {
		return w.fail(ctx, event, err)
	}
	receipt, err := w.provider.Submit(workCtx, bundle)
	if err != nil {
		return w.fail(ctx, event, err)
	}
	if err = validateReceipt(receipt, event.CreatedAt, w.now().UTC()); err != nil {
		return w.fail(ctx, event, err)
	}
	receipt.ReceivedAt = receipt.ReceivedAt.UTC().Truncate(time.Millisecond)
	evidenceDigest := sha256.Sum256(receipt.Evidence)
	evidenceCipher, err := w.protector.Encrypt(workCtx, receipt.Evidence)
	if err != nil || len(evidenceCipher) == 0 || len(evidenceCipher) > (1<<20)+64 {
		return w.fail(ctx, event, errors.New("encrypt public-security receipt evidence failed"))
	}
	receipt.EvidenceSHA256 = hex.EncodeToString(evidenceDigest[:])
	receipt.EvidenceCipher = evidenceCipher
	receipt.Evidence = nil
	return w.store.Complete(workCtx, w.workerID, event, receipt, w.now().UTC().Truncate(time.Millisecond))
}

func (s *GORMStore) LoadBundle(ctx context.Context, protector Protector, event filing.SubmissionOutbox) (SubmissionBundle, error) {
	if event.ID == 0 || event.FilingID == 0 || event.SubmissionID == 0 || event.CreatedAt.IsZero() || !validID(event.EventID, 64) || !validID(event.TenantID, 64) || event.ContractVersion != contractVersion || !validDigest(event.PayloadSHA256) {
		return SubmissionBundle{}, errors.New("invalid Portal filing submission outbox event")
	}
	payloadDigest := sha256.Sum256(event.Payload)
	if hex.EncodeToString(payloadDigest[:]) != event.PayloadSHA256 {
		return SubmissionBundle{}, errors.New("Portal filing submission reference digest mismatch")
	}
	var head filing.Filing
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND id=? AND status=? AND deleted_at IS NULL", event.TenantID, event.FilingID, filing.StatusSubmitting).Take(&head).Error; err != nil {
		return SubmissionBundle{}, err
	}
	var snapshot filing.SubmissionSnapshot
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND filing_id=? AND id=?", event.TenantID, event.FilingID, event.SubmissionID).Take(&snapshot).Error; err != nil {
		return SubmissionBundle{}, err
	}
	var latest filing.SubmissionSnapshot
	if err := s.db.WithContext(ctx).Select("id", "sequence").Where("tenant_id=? AND filing_id=?", event.TenantID, event.FilingID).Order("sequence DESC").Take(&latest).Error; err != nil {
		return SubmissionBundle{}, err
	}
	if latest.ID != snapshot.ID || latest.Sequence != snapshot.Sequence {
		return SubmissionBundle{}, errors.New("Portal filing submission is not the latest immutable snapshot")
	}
	plaintxt, err := protector.Decrypt(ctx, snapshot.CanonicalCipher)
	if err != nil {
		return SubmissionBundle{}, errors.New("decrypt Portal filing snapshot failed")
	}
	snapshotDigest := sha256.Sum256(plaintxt)
	if hex.EncodeToString(snapshotDigest[:]) != snapshot.SnapshotSHA256 {
		return SubmissionBundle{}, errors.New("Portal filing snapshot digest mismatch")
	}
	var materials []filing.Material
	if err = s.db.WithContext(ctx).Where("tenant_id=? AND filing_id=?", event.TenantID, event.FilingID).Order("material_code").Find(&materials).Error; err != nil {
		return SubmissionBundle{}, err
	}
	evidence := make([]MaterialEvidence, 0, len(materials))
	for i := range materials {
		value := materials[i]
		if value.ScanStatus != filing.MaterialClean || value.ScannedAt == nil || !validID(value.ObjectVersion, 256) || !validID(value.ScanReference, 128) || !validDigest(value.SHA256) {
			return SubmissionBundle{}, errors.New("Portal filing material has no immutable CLEAN evidence")
		}
		objectKey, decryptErr := protector.Decrypt(ctx, value.ObjectKeyCipher)
		expectedObjectKey := "portal/filings/" + event.TenantID + "/" + head.PublicID + "/" + value.PublicID
		if decryptErr != nil || string(objectKey) != expectedObjectKey {
			return SubmissionBundle{}, errors.New("Portal filing material object identity is invalid")
		}
		evidence = append(evidence, MaterialEvidence{Code: value.MaterialCode, ObjectKey: string(objectKey), ObjectVersion: value.ObjectVersion, FileName: value.FileName, MIMEType: value.MIMEType, SHA256: value.SHA256, ScanReference: value.ScanReference, SizeBytes: value.SizeBytes, ScannedAt: value.ScannedAt.UTC()})
	}
	return SubmissionBundle{EventID: event.EventID, TenantID: event.TenantID, FilingID: head.PublicID, FilingNo: head.FilingNo, FormVersion: snapshot.FormVersion, SnapshotSHA256: snapshot.SnapshotSHA256, SnapshotJSON: plaintxt, Materials: evidence}, nil
}

func (s *GORMStore) Complete(ctx context.Context, workerID string, event filing.SubmissionOutbox, receipt Receipt, now time.Time) error {
	if !validID(workerID, 128) || !validID(receipt.ID, 128) || !validID(receipt.Authority, 128) || !validDigest(receipt.EvidenceSHA256) || len(receipt.Evidence) != 0 || len(receipt.EvidenceCipher) == 0 || len(receipt.EvidenceCipher) > (1<<20)+64 {
		return errors.New("invalid protected public-security receipt")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current filing.SubmissionOutbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", event.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.TenantID != event.TenantID || current.FilingID != event.FilingID || current.SubmissionID != event.SubmissionID || current.EventID != event.EventID || current.ContractVersion != contractVersion {
			return errLeaseLost
		}
		if current.Status == "SENT" {
			var existing filing.SubmissionReceipt
			if err := tx.Where("tenant_id=? AND filing_id=? AND submission_id=? AND event_id=? AND provider_receipt_id=? AND provider_authority=? AND provider_evidence_sha256=? AND received_at=?", event.TenantID, event.FilingID, event.SubmissionID, event.EventID, receipt.ID, receipt.Authority, receipt.EvidenceSHA256, receipt.ReceivedAt.UTC()).Take(&existing).Error; err != nil {
				return err
			}
			if len(existing.ProviderEvidenceCipher) == 0 {
				return errors.New("stored public-security receipt has no protected evidence")
			}
			var head filing.Filing
			return tx.Where("tenant_id=? AND id=? AND status=? AND deleted_at IS NULL", event.TenantID, event.FilingID, filing.StatusSubmitted).Take(&head).Error
		}
		if current.Status != "PROCESSING" || current.LockedBy != workerID || current.LockedUntil == nil || current.LockedUntil.Before(now) {
			return errLeaseLost
		}
		var head filing.Filing
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=? AND status=? AND deleted_at IS NULL", event.TenantID, event.FilingID, filing.StatusSubmitting).Take(&head).Error; err != nil {
			return err
		}
		proof := &filing.SubmissionReceipt{TenantID: event.TenantID, FilingID: event.FilingID, SubmissionID: event.SubmissionID, EventID: event.EventID, ProviderReceiptID: receipt.ID, ProviderAuthority: receipt.Authority, ProviderEvidenceCipher: append([]byte(nil), receipt.EvidenceCipher...), ProviderEvidenceSHA256: receipt.EvidenceSHA256, ReceivedAt: receipt.ReceivedAt.UTC(), CreatedAt: now}
		if err := tx.Create(proof).Error; err != nil {
			return err
		}
		if result := tx.Model(&filing.Filing{}).Where("tenant_id=? AND id=? AND version=? AND status=?", head.TenantID, head.ID, head.Version, filing.StatusSubmitting).Updates(map[string]any{"status": filing.StatusSubmitted, "updated_by": "public-security-provider", "updated_at": now, "version": gorm.Expr("version+1")}); result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return errLeaseLost
		}
		result := tx.Model(&filing.SubmissionOutbox{}).Where("id=? AND tenant_id=? AND filing_id=? AND submission_id=? AND event_id=? AND status='PROCESSING' AND locked_by=? AND locked_until>=?", event.ID, event.TenantID, event.FilingID, event.SubmissionID, event.EventID, workerID, now).Updates(map[string]any{"status": "SENT", "sent_at": now, "locked_by": "", "locked_until": nil, "next_retry_at": nil, "last_error_summary": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		return nil
	})
}

func (w *Worker) fail(ctx context.Context, event filing.SubmissionOutbox, cause error) error {
	now := w.now().UTC()
	attempt := event.RetryCount + 1
	var next *time.Time
	if attempt < maxAttempts {
		value := now.Add(time.Duration(attempt*attempt) * time.Minute)
		next = &value
	}
	summary := safeError(cause)
	if err := w.store.Fail(ctx, w.workerID, event, summary, attempt, next, now); err != nil {
		return errors.Join(errors.New(summary), err)
	}
	return errors.New(summary)
}

func (s *GORMStore) Fail(ctx context.Context, workerID string, event filing.SubmissionOutbox, summary string, attempt uint32, next *time.Time, now time.Time) error {
	status := "DEAD_LETTER"
	if next != nil {
		status = "PENDING"
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&filing.SubmissionOutbox{}).Where("id=? AND tenant_id=? AND filing_id=? AND submission_id=? AND event_id=? AND status='PROCESSING' AND locked_by=? AND locked_until>=?", event.ID, event.TenantID, event.FilingID, event.SubmissionID, event.EventID, workerID, now).Updates(map[string]any{"status": status, "retry_count": attempt, "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": summary})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errLeaseLost
		}
		if status == "DEAD_LETTER" {
			result = tx.Model(&filing.Filing{}).Where("tenant_id=? AND id=? AND status=?", event.TenantID, event.FilingID, filing.StatusSubmitting).Updates(map[string]any{"status": filing.StatusSubmissionFailed, "updated_by": "public-security-provider", "updated_at": now.UTC(), "version": gorm.Expr("version+1")})
			if result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return errLeaseLost
			}
		}
		return nil
	})
}

func validateReceipt(value Receipt, eventCreatedAt, now time.Time) error {
	if !validID(value.ID, 128) || !validID(value.Authority, 128) || eventCreatedAt.IsZero() || value.ReceivedAt.IsZero() || value.ReceivedAt.Before(eventCreatedAt.Add(-5*time.Minute)) || value.ReceivedAt.After(now.Add(5*time.Minute)) || len(value.Evidence) == 0 || len(value.Evidence) > 1<<20 {
		return errors.New("public-security provider returned invalid receipt evidence")
	}
	return nil
}

func validID(value string, max int) bool {
	return value == strings.TrimSpace(value) && value != "" && len([]byte(value)) <= max && !strings.ContainsAny(value, "\r\n\x00")
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error()))
	if len([]rune(value)) > 1000 {
		value = string([]rune(value)[:1000])
	}
	for _, secretWord := range []string{"token", "secret", "authorization", "password"} {
		if strings.Contains(strings.ToLower(value), secretWord) {
			return "public-security filing submission failed"
		}
	}
	if value == "" {
		return fmt.Sprintf("public-security filing submission failed (%T)", err)
	}
	return value
}
