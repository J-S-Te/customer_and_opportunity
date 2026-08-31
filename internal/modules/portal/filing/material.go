package filing

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"gorm.io/gorm"
)

const filingMaterialMaxBytes = uint64(20 << 20)
const filingMaterialFinalizeLease = 30 * time.Second

var (
	ErrMaterialNotFound       = apperror.New(http.StatusNotFound, "PORTAL_FILING_MATERIAL_NOT_FOUND", "filing material not found")
	ErrMaterialUnavailable    = apperror.New(http.StatusServiceUnavailable, "PORTAL_FILING_MATERIAL_DEPENDENCY_UNAVAILABLE", "filing material storage is unavailable")
	ErrMaterialNotReady       = apperror.New(http.StatusConflict, "PORTAL_FILING_MATERIAL_NOT_READY", "filing material is not ready")
	ErrMaterialContentInvalid = apperror.New(http.StatusUnprocessableEntity, "PORTAL_FILING_MATERIAL_CONTENT_INVALID", "filing material content does not match the declared metadata")
)

var materialMIMEByExtension = map[string]string{
	"pdf": "application/pdf", "png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg",
}

var validMaterialCodes = map[string]struct{}{
	"NETWORK_TOPOLOGY": {}, "SECURITY_GOVERNANCE": {}, "SECURITY_DESIGN": {},
	"SECURITY_PRODUCTS": {}, "SECURITY_SERVICES": {}, "AUTHORITY_GUIDANCE": {},
	"CLASSIFICATION_REPORT": {},
}

type MaterialUploadCommand struct {
	MaterialCode, FileName, MIMEType, SHA256, IdempotencyKey string
	SizeBytes                                                uint64
}

type MaterialUploadGrant struct {
	Material   MaterialView `json:"material"`
	UploadURL  string       `json:"upload_url"`
	UploadMode string       `json:"upload_mode"`
	ExpiresAt  time.Time    `json:"expires_at"`
}

type MaterialObjectMetadata struct {
	ObjectVersion, MIMEType, SHA256 string
	SizeBytes                       uint64
}

// MaterialObjectStore 与供应方无关，每个操作都必须绑定不可变对象版本；API 不接受任意回调 URL。
type MaterialObjectStore interface {
	Available() bool
	CreateUpload(context.Context, string, string, uint64, string, string) (string, time.Time, error)
	Finalize(context.Context, string) (MaterialObjectMetadata, error)
	OpenVerified(context.Context, string, string, string, uint64) (io.ReadCloser, error)
}

// InternalMaterialContentStore 表示文件内容必须通过 Portal 受控代理写入，而不是由浏览器
// 直接访问对象存储。实现必须验证大小、摘要和媒体类型，并保证不会覆盖既有对象。
type InternalMaterialContentStore interface {
	PutVerified(context.Context, string, io.Reader, uint64, string, string) error
}

type MaterialScanner interface {
	Available() bool
	Submit(context.Context, string, string, string, string, uint64, string) (string, error)
}

type immediateMaterialScanner interface {
	ImmediateStatus(string) (string, bool)
}

type MaterialObjectProtector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}

type UnavailableMaterialObjectStore struct{}

func (UnavailableMaterialObjectStore) Available() bool { return false }
func (UnavailableMaterialObjectStore) CreateUpload(context.Context, string, string, uint64, string, string) (string, time.Time, error) {
	return "", time.Time{}, ErrMaterialUnavailable
}
func (UnavailableMaterialObjectStore) Finalize(context.Context, string) (MaterialObjectMetadata, error) {
	return MaterialObjectMetadata{}, ErrMaterialUnavailable
}
func (UnavailableMaterialObjectStore) OpenVerified(context.Context, string, string, string, uint64) (io.ReadCloser, error) {
	return nil, ErrMaterialUnavailable
}

type UnavailableMaterialScanner struct{}

func (UnavailableMaterialScanner) Available() bool { return false }
func (UnavailableMaterialScanner) Submit(context.Context, string, string, string, string, uint64, string) (string, error) {
	return "", ErrMaterialUnavailable
}

type MaterialService struct {
	repo      Repository
	protector MaterialObjectProtector
	store     MaterialObjectStore
	scanner   MaterialScanner
	clock     Clock
	ids       IDGenerator
}

func NewMaterialService(repo Repository, protector MaterialObjectProtector, store MaterialObjectStore, scanner MaterialScanner, clock Clock, ids IDGenerator) *MaterialService {
	if store == nil {
		store = UnavailableMaterialObjectStore{}
	}
	if scanner == nil {
		scanner = UnavailableMaterialScanner{}
	}
	return &MaterialService{repo: repo, protector: protector, store: store, scanner: scanner, clock: clock, ids: ids}
}

// RuntimeAvailable 仅报告上传链路依赖的本地注入状态，不对对象存储或扫描器发起网络探测。
func (s *MaterialService) RuntimeAvailable() bool {
	return s != nil && s.protector != nil && s.store != nil && s.scanner != nil && s.store.Available() && s.scanner.Available()
}

func (s *MaterialService) CreateUpload(ctx context.Context, actor Actor, filingPublicID string, command MaterialUploadCommand) (*MaterialUploadGrant, error) {
	filingPublicID, command.MaterialCode = strings.TrimSpace(filingPublicID), strings.TrimSpace(command.MaterialCode)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	name, mediaType, digest, err := validateMaterial(command)
	if !validActor(actor) || !validPublicID(filingPublicID) || err != nil || !validIdempotencyKey(command.IdempotencyKey) {
		return nil, ErrValidation
	}
	filing, err := s.repo.FindOwned(ctx, actor, filingPublicID)
	if err != nil {
		return nil, err
	}
	if filing.Status != StatusDraft {
		return nil, ErrLocked
	}
	if !s.store.Available() || !s.scanner.Available() || s.protector == nil {
		return nil, ErrMaterialUnavailable
	}
	createKeyHash := materialDigest(command.IdempotencyKey)
	requestHash := materialRequestHash(filing.ID, command.MaterialCode, name, mediaType, command.SizeBytes, digest)
	var material *Material
	if existing, findErr := s.repo.FindMaterialByCreate(ctx, actor.TenantID, actor.AccountID, createKeyHash); findErr == nil {
		material, err = validateMaterialCreateReplay(existing, actor, filing.ID, command, name, mediaType, digest, createKeyHash, requestHash)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(findErr, ErrMaterialNotFound) {
		return nil, findErr
	}
	if material == nil {
		if _, err = s.repo.FindMaterial(ctx, actor.TenantID, filing.ID, command.MaterialCode); err == nil {
			return nil, ErrVersionConflict
		} else if !errors.Is(err, ErrMaterialNotFound) {
			return nil, err
		}
		publicID := strings.TrimSpace(s.ids.NewID())
		if !validGeneratedID(publicID) {
			return nil, ErrMaterialUnavailable
		}
		objectKey := "portal/filings/" + actor.TenantID + "/" + filing.PublicID + "/" + publicID
		objectCipher, encryptErr := s.protector.Encrypt(ctx, []byte(objectKey))
		if encryptErr != nil || len(objectCipher) == 0 || len(objectCipher) > 1024 {
			return nil, ErrMaterialUnavailable
		}
		now := s.clock.Now().UTC()
		material = &Material{TenantID: actor.TenantID, PublicID: publicID, FilingID: filing.ID, MaterialCode: command.MaterialCode, ObjectKeyCipher: objectCipher, FileName: name, MIMEType: mediaType, SizeBytes: command.SizeBytes, SHA256: digest, ScanStatus: MaterialPendingUpload, CreateActorID: actor.AccountID, CreateKeyHash: createKeyHash, CreateRequestHash: requestHash, CreatedBy: actor.AccountID, UpdatedBy: actor.AccountID, CreatedAt: now, UpdatedAt: now, Version: 1}
		if err = s.repo.CreateMaterial(ctx, material); err != nil {
			if !isDuplicateMaterialCreate(err) {
				return nil, err
			}
			// 两个请求完成前置读取后，并发插入可能赢得账号幂等键或材料代码唯一约束。
			// MySQL 会等待获胜事务后返回 1062，因此重新读取并核验已提交结果，不暴露驱动错误或创建第二个对象引用。
			material, err = s.recoverDuplicateMaterialCreate(ctx, actor, filing.ID, command, name, mediaType, digest, createKeyHash, requestHash)
			if err != nil {
				return nil, err
			}
		}
	}
	plainObjectKey, err := s.protector.Decrypt(ctx, material.ObjectKeyCipher)
	if err != nil || !validMaterialObjectKey(string(plainObjectKey), actor.TenantID, filing.PublicID, material.PublicID) {
		return nil, ErrMaterialUnavailable
	}
	now := s.clock.Now().UTC()
	uploadURL, expiresAt, err := s.store.CreateUpload(ctx, string(plainObjectKey), mediaType, command.SizeBytes, digest, name)
	if err != nil || strings.TrimSpace(uploadURL) == "" || !expiresAt.After(now) {
		return nil, ErrMaterialUnavailable
	}
	uploadMode := "DIRECT"
	if _, ok := s.store.(InternalMaterialContentStore); ok {
		uploadMode = "INTERNAL"
		uploadURL = "/filings/" + filing.PublicID + "/materials/" + material.PublicID + "/content"
	}
	return &MaterialUploadGrant{Material: materialView(material), UploadURL: uploadURL, UploadMode: uploadMode, ExpiresAt: expiresAt}, nil
}

// UploadContent 只允许备案所有者把与预登记元数据完全一致的内容写入内部文件网关。
// 上传前使用版本号和短租约串行化并发请求；存储写入成功后再记录 uploaded_at，重复写入、
// 跨租户材料、错误 MIME/大小/摘要或非 PENDING_UPLOAD 状态均不会覆盖对象。
func (s *MaterialService) UploadContent(ctx context.Context, actor Actor, filingPublicID, materialPublicID string, expectedVersion uint64, body io.Reader, contentType string, contentLength int64) (*MaterialView, error) {
	filingPublicID, materialPublicID = strings.TrimSpace(filingPublicID), strings.TrimSpace(materialPublicID)
	if !validActor(actor) || !validPublicID(filingPublicID) || !validPublicID(materialPublicID) || expectedVersion == 0 || body == nil {
		return nil, ErrValidation
	}
	filing, err := s.repo.FindOwned(ctx, actor, filingPublicID)
	if err != nil {
		return nil, err
	}
	if filing.Status != StatusDraft {
		return nil, ErrLocked
	}
	store, ok := s.store.(InternalMaterialContentStore)
	if !ok || !s.store.Available() || s.protector == nil {
		return nil, ErrMaterialUnavailable
	}
	material, err := s.repo.FindMaterialByPublicIDForUpdate(ctx, actor.TenantID, filing.ID, materialPublicID)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	if material.CreateActorID != actor.AccountID || material.ScanStatus != MaterialPendingUpload || material.UploadedAt != nil || material.Version != expectedVersion || material.FinalizeLeaseUntil != nil && material.FinalizeLeaseUntil.After(now) {
		return nil, ErrMaterialNotReady
	}
	if contentLength < 0 || uint64(contentLength) != material.SizeBytes || canonicalMaterialMIME(contentType) != material.MIMEType {
		return nil, ErrMaterialContentInvalid
	}

	reader := bufio.NewReader(io.LimitReader(body, int64(material.SizeBytes)+1))
	peekSize := int(material.SizeBytes)
	if peekSize > 512 {
		peekSize = 512
	}
	header, peekErr := reader.Peek(peekSize)
	if peekErr != nil || canonicalMaterialMIME(http.DetectContentType(header)) != material.MIMEType {
		return nil, ErrMaterialContentInvalid
	}

	leaseUntil := now.Add(filingMaterialFinalizeLease)
	if err = s.repo.UpdateMaterial(ctx, material, material.Version, map[string]any{"finalize_lease_until": leaseUntil, "updated_by": actor.AccountID, "updated_at": now}); err != nil {
		return nil, err
	}
	material.FinalizeLeaseUntil = &leaseUntil
	plainObjectKey, err := s.protector.Decrypt(ctx, material.ObjectKeyCipher)
	if err != nil || !validMaterialObjectKey(string(plainObjectKey), actor.TenantID, filing.PublicID, material.PublicID) {
		s.releaseContentUpload(ctx, material, actor.AccountID)
		return nil, ErrMaterialUnavailable
	}
	if err = store.PutVerified(ctx, string(plainObjectKey), reader, material.SizeBytes, material.SHA256, material.MIMEType); err != nil {
		s.releaseContentUpload(ctx, material, actor.AccountID)
		if errors.Is(err, ErrMaterialContentInvalid) || errors.Is(err, ErrValidation) {
			return nil, ErrMaterialContentInvalid
		}
		return nil, ErrMaterialUnavailable
	}
	uploadedAt := s.clock.Now().UTC()
	if err = s.repo.UpdateMaterial(ctx, material, material.Version, map[string]any{"uploaded_at": uploadedAt, "finalize_lease_until": nil, "updated_by": actor.AccountID, "updated_at": uploadedAt}); err != nil {
		return nil, err
	}
	material.UploadedAt, material.FinalizeLeaseUntil, material.UpdatedAt = &uploadedAt, nil, uploadedAt
	view := materialView(material)
	return &view, nil
}

func (s *MaterialService) releaseContentUpload(ctx context.Context, material *Material, actorID string) {
	_ = s.repo.UpdateMaterial(ctx, material, material.Version, map[string]any{"finalize_lease_until": nil, "updated_by": actorID, "updated_at": s.clock.Now().UTC()})
}

func (s *MaterialService) recoverDuplicateMaterialCreate(ctx context.Context, actor Actor, filingID uint64, command MaterialUploadCommand, name, mediaType, digest, createKeyHash, requestHash string) (*Material, error) {
	winner, err := s.repo.FindMaterialByCreate(ctx, actor.TenantID, actor.AccountID, createKeyHash)
	if err == nil {
		return validateMaterialCreateReplay(winner, actor, filingID, command, name, mediaType, digest, createKeyHash, requestHash)
	}
	if !errors.Is(err, ErrMaterialNotFound) {
		return nil, err
	}
	if _, err = s.repo.FindMaterial(ctx, actor.TenantID, filingID, command.MaterialCode); err == nil {
		return nil, ErrVersionConflict
	}
	if !errors.Is(err, ErrMaterialNotFound) {
		return nil, err
	}
	// 剩余唯一键是生成的公开 ID；碰撞作为稳定冲突处理，不泄露驱动重复键文本。
	return nil, ErrVersionConflict
}

func validateMaterialCreateReplay(existing *Material, actor Actor, filingID uint64, command MaterialUploadCommand, name, mediaType, digest, createKeyHash, requestHash string) (*Material, error) {
	if existing == nil || existing.TenantID != actor.TenantID || existing.CreateActorID != actor.AccountID || existing.CreateKeyHash != createKeyHash ||
		existing.FilingID != filingID || existing.MaterialCode != command.MaterialCode || existing.FileName != name || existing.MIMEType != mediaType ||
		existing.SizeBytes != command.SizeBytes || !strings.EqualFold(existing.SHA256, digest) || existing.CreateRequestHash != requestHash {
		return nil, ErrIdempotencyConflict
	}
	if existing.ScanStatus != MaterialPendingUpload {
		return nil, ErrMaterialNotReady
	}
	return existing, nil
}

func isDuplicateMaterialCreate(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func (s *MaterialService) CompleteUpload(ctx context.Context, actor Actor, filingPublicID, materialPublicID string, expectedVersion uint64) (*MaterialView, error) {
	filing, err := s.repo.FindOwned(ctx, actor, strings.TrimSpace(filingPublicID))
	if err != nil {
		return nil, err
	}
	if filing.Status != StatusDraft {
		return nil, ErrLocked
	}
	if !s.store.Available() || !s.scanner.Available() || s.protector == nil {
		return nil, ErrMaterialUnavailable
	}
	material, err := s.repo.FindMaterialByPublicIDForUpdate(ctx, actor.TenantID, filing.ID, strings.TrimSpace(materialPublicID))
	if err != nil {
		return nil, err
	}
	if material.ScanStatus == MaterialScanning || material.ScanStatus == MaterialClean {
		view := materialView(material)
		return &view, nil
	}
	now := s.clock.Now().UTC()
	if material.ScanStatus == MaterialFinalizing && (material.FinalizeLeaseUntil == nil || material.FinalizeLeaseUntil.After(now)) {
		return nil, ErrMaterialNotReady
	}
	if material.ScanStatus != MaterialPendingUpload && material.ScanStatus != MaterialFinalizing || material.ScanStatus == MaterialPendingUpload && (material.Version != expectedVersion || material.UploadedAt == nil || material.FinalizeLeaseUntil != nil && material.FinalizeLeaseUntil.After(now)) {
		return nil, ErrMaterialNotReady
	}
	leaseUntil := now.Add(filingMaterialFinalizeLease)
	if err = s.repo.UpdateMaterial(ctx, material, material.Version, map[string]any{"scan_status": MaterialFinalizing, "finalize_lease_until": leaseUntil, "updated_by": actor.AccountID, "updated_at": now}); err != nil {
		return nil, err
	}
	material.ScanStatus, material.FinalizeLeaseUntil = MaterialFinalizing, &leaseUntil
	plainObjectKey, err := s.protector.Decrypt(ctx, material.ObjectKeyCipher)
	if err != nil || !validMaterialObjectKey(string(plainObjectKey), actor.TenantID, filing.PublicID, material.PublicID) {
		s.releaseFinalize(ctx, material, actor.AccountID)
		return nil, ErrMaterialUnavailable
	}
	metadata, err := s.store.Finalize(ctx, string(plainObjectKey))
	if err != nil || strings.TrimSpace(metadata.ObjectVersion) == "" || metadata.SizeBytes != material.SizeBytes || canonicalMaterialMIME(metadata.MIMEType) != material.MIMEType || !strings.EqualFold(metadata.SHA256, material.SHA256) {
		s.releaseFinalize(ctx, material, actor.AccountID)
		return nil, ErrMaterialUnavailable
	}
	scanReference, err := s.scanner.Submit(ctx, material.PublicID, string(plainObjectKey), metadata.ObjectVersion, material.SHA256, material.SizeBytes, material.MIMEType)
	if err != nil || strings.TrimSpace(scanReference) == "" {
		s.releaseFinalize(ctx, material, actor.AccountID)
		return nil, ErrMaterialUnavailable
	}
	now = s.clock.Now().UTC()
	scanStatus := MaterialScanning
	fields := map[string]any{"object_version": metadata.ObjectVersion, "scan_reference": strings.TrimSpace(scanReference), "scan_status": scanStatus, "uploaded_at": now, "finalize_lease_until": nil, "updated_by": actor.AccountID, "updated_at": now}
	var scannedAt *time.Time
	if immediate, ok := s.scanner.(immediateMaterialScanner); ok {
		if status, complete := immediate.ImmediateStatus(scanReference); complete {
			scanStatus = status
			fields["scan_status"] = status
			fields["scanned_at"] = now
			scannedAt = &now
		}
	}
	if err = s.repo.UpdateMaterial(ctx, material, material.Version, fields); err != nil {
		return nil, err
	}
	material.ObjectVersion, material.ScanReference, material.ScanStatus, material.UploadedAt, material.ScannedAt, material.UpdatedAt = metadata.ObjectVersion, strings.TrimSpace(scanReference), scanStatus, &now, scannedAt, now
	view := materialView(material)
	return &view, nil
}

func (s *MaterialService) releaseFinalize(ctx context.Context, material *Material, actorID string) {
	_ = s.repo.UpdateMaterial(ctx, material, material.Version, map[string]any{"scan_status": MaterialPendingUpload, "finalize_lease_until": nil, "updated_by": actorID, "updated_at": s.clock.Now().UTC()})
}

type MaterialScanEvent struct {
	MaterialID, ScanReference, Status string
	OccurredAt                        time.Time
}

func (s *MaterialService) ApplyScan(ctx context.Context, tenant string, event MaterialScanEvent) (*MaterialView, error) {
	status, tenant := strings.TrimSpace(event.Status), strings.TrimSpace(tenant)
	if tenant == "" || !validPublicID(event.MaterialID) || (status != MaterialClean && status != MaterialRejected && status != MaterialScanFailed) || event.OccurredAt.IsZero() {
		return nil, ErrValidation
	}
	var result MaterialView
	err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		material, err := s.repo.FindMaterialForScanUpdate(tx, tenant, strings.TrimSpace(event.MaterialID))
		if err != nil {
			return err
		}
		if material.ScanReference != strings.TrimSpace(event.ScanReference) {
			return ErrMaterialNotFound
		}
		if material.ScanStatus == status {
			result = materialView(material)
			return nil
		}
		if material.ScanStatus != MaterialScanning || material.UploadedAt == nil || event.OccurredAt.Before(material.UploadedAt.Add(-time.Second)) || event.OccurredAt.After(s.clock.Now().Add(5*time.Minute)) {
			return ErrMaterialNotReady
		}
		occurredAt := event.OccurredAt.UTC()
		if err = s.repo.UpdateMaterial(tx, material, material.Version, map[string]any{"scan_status": status, "scanned_at": occurredAt, "updated_by": "material-scanner", "updated_at": occurredAt}); err != nil {
			return err
		}
		material.ScanStatus, material.ScannedAt, material.UpdatedAt = status, &occurredAt, occurredAt
		result = materialView(material)
		return nil
	})
	return &result, err
}

func validateMaterial(command MaterialUploadCommand) (string, string, string, error) {
	if _, ok := validMaterialCodes[command.MaterialCode]; !ok || command.SizeBytes == 0 || command.SizeBytes > filingMaterialMaxBytes {
		return "", "", "", ErrValidation
	}
	name := strings.TrimSpace(command.FileName)
	if name == "" || len([]byte(name)) > 255 || filepath.Base(name) != name || strings.Contains(name, "\\") {
		return "", "", "", ErrValidation
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", "", "", ErrValidation
		}
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	expected, ok := materialMIMEByExtension[extension]
	mediaType := canonicalMaterialMIME(command.MIMEType)
	if !ok || expected != mediaType {
		return "", "", "", ErrValidation
	}
	digest := strings.ToLower(strings.TrimSpace(command.SHA256))
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", "", "", ErrValidation
	}
	return name, mediaType, digest, nil
}

func canonicalMaterialMIME(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

func validMaterialObjectKey(value, tenant, filingPublicID, materialPublicID string) bool {
	return value == "portal/filings/"+tenant+"/"+filingPublicID+"/"+materialPublicID
}

func materialView(value *Material) MaterialView {
	return MaterialView{ID: value.PublicID, Code: value.MaterialCode, FileName: value.FileName, MIMEType: value.MIMEType, SizeBytes: value.SizeBytes, SHA256: value.SHA256, ScanStatus: value.ScanStatus, UploadedAt: value.UploadedAt, ScannedAt: value.ScannedAt, Version: value.Version}
}

func materialDigest(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(digest[:])
}

func materialRequestHash(filingID uint64, code, name, mediaType string, size uint64, digest string) string {
	raw, _ := json.Marshal([]any{filingID, code, name, mediaType, size, digest})
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}
