// Package reportsecurity 实现 Portal 的可信报告文件边界。
// 文件静态加密；仅有源回调绝不构成扫描证据。
package reportsecurity

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

const maxPDFBytes = 50 << 20
const scannerVersion = "portal-pdf-static-v1"

var storeIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrUnavailable = errors.New("trusted report dependency unavailable")
var ErrInvalid = errors.New("trusted report security evidence invalid")

type manifest struct {
	SourceVersion                   string
	TenantID, EventID               string
	CustomerID, RequestID, Revision uint64
	Descriptor                      report.FileDescriptor
	PDF                             []byte
	ScannedAt                       time.Time
}

type EncryptedStore struct {
	root           *os.Root
	aead           cipher.AEAD
	tenant, keyRef string
	protector      report.DescriptorProtector
}

func NewEncryptedStore(directory string, key []byte, tenant string, protector report.DescriptorProtector) (*EncryptedStore, error) {
	if len(key) != 32 || strings.TrimSpace(tenant) == "" || protector == nil || !filepath.IsAbs(directory) {
		return nil, ErrInvalid
	}
	if info, err := os.Lstat(directory); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, ErrInvalid
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	fingerprint := sha256.Sum256(key)
	return &EncryptedStore{root: root, aead: aead, tenant: tenant, keyRef: "portal-report-aes-v1:" + hex.EncodeToString(fingerprint[:8]), protector: protector}, nil
}
func (s *EncryptedStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}
func (s *EncryptedStore) Available() bool {
	return s != nil && s.root != nil && s.aead != nil && s.protector != nil
}

func manifestID(job report.IngestJob) string {
	identity := fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%s", job.TenantID, job.CustomerID, job.RequestID, job.ReportRevision, job.EventID)
	hash := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(hash[:])
}

func (s *EncryptedStore) Persist(ctx context.Context, job report.IngestJob, descriptor report.FileDescriptor, pdf []byte) (report.IngestResult, error) {
	return s.PersistSource(ctx, job, descriptor, pdf, descriptor.FileHash)
}
func (s *EncryptedStore) PersistSource(ctx context.Context, job report.IngestJob, descriptor report.FileDescriptor, pdf []byte, sourceVersion string) (report.IngestResult, error) {
	if !safeText(sourceVersion, 256) || !s.Available() || job.TenantID != s.tenant || job.CustomerID == 0 || job.RequestID == 0 || job.ReportRevision == 0 || job.EventID == "" {
		return report.IngestResult{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return report.IngestResult{}, err
	}
	if err := ValidatePDF(ctx, pdf, descriptor); err != nil {
		return report.IngestResult{}, err
	}
	id := manifestID(job)
	existing, err := s.read(ctx, id)
	if err == nil {
		if !sameManifest(existing, job, descriptor) || existing.SourceVersion != sourceVersion {
			return report.IngestResult{}, ErrInvalid
		}
		return s.receipt(ctx, id, existing)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return report.IngestResult{}, err
	}
	value := manifest{TenantID: job.TenantID, EventID: job.EventID, SourceVersion: sourceVersion, CustomerID: job.CustomerID, RequestID: job.RequestID, Revision: job.ReportRevision, Descriptor: descriptor, PDF: pdf, ScannedAt: time.Now().UTC().Truncate(time.Millisecond)}
	plain, err := json.Marshal(value)
	if err != nil {
		return report.IngestResult{}, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return report.IngestResult{}, err
	}
	sealed := s.aead.Seal(nonce, nonce, plain, []byte("portal-report-v1:"+id))
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return report.IngestResult{}, err
	}
	temp := ".incoming-" + hex.EncodeToString(random)
	file, err := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return report.IngestResult{}, err
	}
	defer s.root.Remove(temp)
	n, writeErr := file.Write(sealed)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(sealed) {
		return report.IngestResult{}, ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return report.IngestResult{}, err
	}
	// 发布时禁止替换。并发重投必须匹配首次
	// 提交的描述，重试不得替换已接受的报告。
	if err = s.root.Link(temp, id+".report"); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return report.IngestResult{}, err
		}
		other, readErr := s.read(ctx, id)
		if readErr != nil || (!sameManifest(other, job, descriptor) || other.SourceVersion != sourceVersion) {
			return report.IngestResult{}, ErrInvalid
		}
		return s.receipt(ctx, id, other)
	}
	return s.receipt(ctx, id, &value)
}
func sameManifest(m *manifest, j report.IngestJob, d report.FileDescriptor) bool {
	return m != nil && m.TenantID == j.TenantID && m.EventID == j.EventID && m.CustomerID == j.CustomerID && m.RequestID == j.RequestID && m.Revision == j.ReportRevision && m.Descriptor == d
}
func (s *EncryptedStore) receipt(ctx context.Context, id string, m *manifest) (report.IngestResult, error) {
	directory, err := s.root.Open(".")
	if err != nil {
		return report.IngestResult{}, err
	}
	syncErr, closeErr := directory.Sync(), directory.Close()
	if syncErr != nil || closeErr != nil {
		return report.IngestResult{}, ErrUnavailable
	}
	key, err := s.protector.Encrypt(ctx, []byte(id))
	if err != nil {
		return report.IngestResult{}, err
	}
	return report.IngestResult{ObjectKeyCipher: key, ObjectVersion: m.SourceVersion, EncryptionKeyRef: s.keyRef, EncryptionAlgorithm: "AES-256-GCM", ScanStatus: "CLEAN", ScanReference: scannerVersion + ":" + m.Descriptor.FileHash, ScannedAt: m.ScannedAt, WatermarkStatus: "PER_DOWNLOAD_REQUIRED"}, nil
}
func (s *EncryptedStore) read(ctx context.Context, id string) (*manifest, error) {
	if !s.Available() || !storeIDPattern.MatchString(id) {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := s.root.Lstat(id + ".report")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < int64(s.aead.NonceSize()+s.aead.Overhead()) || info.Size() > 75<<20 {
		return nil, ErrInvalid
	}
	file, err := s.root.Open(id + ".report")
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 75<<20+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() || len(raw) > 75<<20 {
		return nil, ErrInvalid
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte("portal-report-v1:"+id))
	if err != nil {
		return nil, ErrInvalid
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return nil, ErrInvalid
	}
	if m.TenantID != s.tenant || !safeText(m.SourceVersion, 256) || m.ScannedAt.IsZero() || m.ScannedAt.After(time.Now().Add(5*time.Minute)) {
		return nil, ErrInvalid
	}
	job := report.IngestJob{TenantID: m.TenantID, EventID: m.EventID, CustomerID: m.CustomerID, RequestID: m.RequestID, ReportRevision: m.Revision}
	if manifestID(job) != id || ValidatePDF(ctx, m.PDF, m.Descriptor) != nil {
		return nil, ErrInvalid
	}
	return &m, nil
}
func (s *EncryptedStore) OpenVerified(ctx context.Context, f *report.File) (report.PreparedDownload, error) {
	if !s.Available() || f == nil || f.TenantID != s.tenant || f.EncryptionKeyRef != s.keyRef || f.EncryptionAlgorithm != "AES-256-GCM" || f.ScanStatus != "CLEAN" {
		return report.PreparedDownload{}, ErrInvalid
	}
	id, err := s.protector.Decrypt(ctx, f.ObjectKeyCipher)
	if err != nil {
		return report.PreparedDownload{}, ErrInvalid
	}
	m, err := s.read(ctx, string(id))
	if err != nil {
		return report.PreparedDownload{}, err
	}
	d := m.Descriptor
	if m.RequestID != f.RequestID || m.Revision != f.ReportRevision || d.FileName != f.FileName || d.MIME != f.MIME || d.Size != f.Size || d.FileHash != f.FileHash || f.ObjectVersion != m.SourceVersion || f.ScanReference != scannerVersion+":"+d.FileHash || f.ScannedAt == nil || !m.ScannedAt.Equal(*f.ScannedAt) {
		return report.PreparedDownload{}, ErrInvalid
	}
	return report.PreparedDownload{Reader: io.NopCloser(bytes.NewReader(m.PDF)), FileName: d.FileName, MIME: d.MIME, Size: d.Size, FileHash: d.FileHash}, nil
}
