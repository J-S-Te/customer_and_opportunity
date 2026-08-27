package opportunity

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	localUploadTTL       = 10 * time.Minute
	maxArchiveEntries    = 2048
	maxArchiveExpanded   = uint64(100 << 20)
	maxArchiveRatio      = uint64(100)
	maxImagePixels       = uint64(40_000_000)
	localScannerVersion  = "code-scan-v1"
	localUploadExtension = ".upload"
)

// LocalAttachmentObjectStore 把未扫描文件放入仅服务进程可读写的隔离目录。文件名完全
// 来自服务端生成的对象键，客户端文件名永不参与路径拼接；终结后改为只读且按摘要校验。
type LocalAttachmentObjectStore struct{ root string }

type localAttachmentMetadata struct {
	SizeBytes uint64 `json:"size_bytes"`
	MIMEType  string `json:"mime_type"`
	SHA256    string `json:"sha256"`
}

func NewLocalAttachmentObjectStore(root string) (*LocalAttachmentObjectStore, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("attachment local root must be an absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create attachment local root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect attachment local root: %w", err)
	}
	return &LocalAttachmentObjectStore{root: root}, nil
}

func (s *LocalAttachmentObjectStore) Available() bool { return s != nil && s.root != "" }
func (s *LocalAttachmentObjectStore) CreateUpload(context.Context, string, string, uint64, string, string) (AttachmentUploadGrant, error) {
	if !s.Available() {
		return AttachmentUploadGrant{}, ErrAttachmentUnavailable
	}
	return AttachmentUploadGrant{URL: "internal://crm-attachment-upload", ExpiresAt: time.Now().UTC().Add(localUploadTTL)}, nil
}

func (s *LocalAttachmentObjectStore) safePath(key, suffix string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrAttachmentInvalid
	}
	value := filepath.Join(s.root, clean) + suffix
	rel, err := filepath.Rel(s.root, value)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrAttachmentInvalid
	}
	return value, nil
}

func (s *LocalAttachmentObjectStore) PutVerified(_ context.Context, key string, body io.Reader, size uint64, digest, media string) error {
	path, err := s.safePath(key, localUploadExtension)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	if _, statErr := os.Stat(path); statErr == nil {
		metadata, metaErr := localFileMetadata(path)
		if metaErr == nil && metadata.SizeBytes == size && metadata.MIMEType == canonicalMIME(media) && strings.EqualFold(metadata.SHA256, digest) {
			return nil
		}
		return ErrAttachmentInvalid
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".incoming-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(body, int64(size)+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || written != int64(size) || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), digest) {
		return ErrAttachmentInvalid
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	meta, _ := json.Marshal(localAttachmentMetadata{SizeBytes: size, MIMEType: media, SHA256: strings.ToLower(digest)})
	if err = os.WriteFile(path+".meta", meta, 0o600); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func (s *LocalAttachmentObjectStore) Finalize(_ context.Context, key string) (AttachmentObjectMetadata, error) {
	upload, err := s.safePath(key, localUploadExtension)
	if err != nil {
		return AttachmentObjectMetadata{}, err
	}
	final, err := s.safePath(key, "")
	if err != nil {
		return AttachmentObjectMetadata{}, err
	}
	if _, statErr := os.Stat(final); statErr == nil {
		return localFileMetadata(final)
	}
	if err = os.Rename(upload, final); err != nil {
		return AttachmentObjectMetadata{}, err
	}
	if err = os.Rename(upload+".meta", final+".meta"); err != nil {
		_ = os.Rename(final, upload)
		return AttachmentObjectMetadata{}, err
	}
	if err = os.Chmod(final, 0o400); err != nil {
		return AttachmentObjectMetadata{}, err
	}
	_ = os.Chmod(final+".meta", 0o400)
	return localFileMetadata(final)
}

func localFileMetadata(path string) (AttachmentObjectMetadata, error) {
	metaBody, err := os.ReadFile(path + ".meta")
	if err != nil {
		return AttachmentObjectMetadata{}, err
	}
	var expected localAttachmentMetadata
	if json.Unmarshal(metaBody, &expected) != nil || expected.SizeBytes == 0 || canonicalMIME(expected.MIMEType) == "" {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return AttachmentObjectMetadata{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil || size <= 0 || uint64(size) != expected.SizeBytes {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(digest, expected.SHA256) {
		return AttachmentObjectMetadata{}, ErrAttachmentInvalid
	}
	return AttachmentObjectMetadata{ObjectVersion: digest, SizeBytes: uint64(size), MIMEType: canonicalMIME(expected.MIMEType), SHA256: digest}, nil
}

func (s *LocalAttachmentObjectStore) OpenVerified(_ context.Context, key, version, digest string, size uint64) (io.ReadCloser, error) {
	path, err := s.safePath(key, "")
	if err != nil {
		return nil, err
	}
	metadata, err := localFileMetadata(path)
	if err != nil || metadata.SizeBytes != size || !strings.EqualFold(metadata.SHA256, digest) || metadata.ObjectVersion != version {
		return nil, ErrAttachmentInvalid
	}
	return os.Open(path)
}

// CodeAttachmentScanner 对任意对象存储中的内容执行进程内静态校验；
// 它既可独立作为本地部署的扫描器，也可作为外部引擎扫描器之外的第一道防线。
type CodeAttachmentScanner struct{ store AttachmentObjectStore }

func NewCodeAttachmentScanner(store AttachmentObjectStore) *CodeAttachmentScanner {
	return &CodeAttachmentScanner{store: store}
}
func (s *CodeAttachmentScanner) Available() bool {
	return s != nil && s.store != nil && s.store.Available()
}
func (s *CodeAttachmentScanner) Submit(ctx context.Context, _ string, key, version, digest string, size uint64, media string) (string, error) {
	reader, err := s.store.OpenVerified(ctx, key, version, digest, size)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, int64(size)+1))
	if err != nil || uint64(len(content)) != size {
		return "", ErrAttachmentInvalid
	}
	status := AttachmentClean
	if scanAttachmentBytes(content, media) != nil {
		status = AttachmentRejected
	}
	return localScannerVersion + ":" + strings.ToLower(status) + ":" + digest, nil
}
func (*CodeAttachmentScanner) ImmediateStatus(reference string) (string, bool) {
	parts := strings.Split(reference, ":")
	if len(parts) != 3 || parts[0] != localScannerVersion {
		return "", false
	}
	status := strings.ToUpper(parts[1])
	return status, status == AttachmentClean || status == AttachmentRejected
}

func scanAttachmentBytes(content []byte, media string) error {
	lower := bytes.ToLower(content)
	if bytes.Contains(lower, []byte("x5o!p%@ap[4\\pzx54(p^)7cc)7}$eicar-standard-antivirus-test-file!$h+h*")) ||
		bytes.HasPrefix(content, []byte("MZ")) || bytes.HasPrefix(content, []byte("\x7fELF")) ||
		bytes.HasPrefix(lower, []byte("#!")) || bytes.Contains(lower, []byte("<script")) {
		return ErrAttachmentRejected
	}
	switch media {
	case "application/pdf":
		if !bytes.HasPrefix(content, []byte("%PDF-")) || !bytes.Contains(content[maxInt(0, len(content)-2048):], []byte("%%EOF")) {
			return ErrAttachmentRejected
		}
		for _, marker := range [][]byte{[]byte("/javascript"), []byte("/js"), []byte("/launch"), []byte("/embeddedfile"), []byte("/openaction"), []byte("/aa"), []byte("/encrypt")} {
			if bytes.Contains(lower, marker) {
				return ErrAttachmentRejected
			}
		}
	case "image/png", "image/jpeg":
		config, detected, err := image.DecodeConfig(bytes.NewReader(content))
		if err != nil || detected == "" || uint64(config.Width)*uint64(config.Height) > maxImagePixels || config.Width <= 0 || config.Height <= 0 {
			return ErrAttachmentRejected
		}
		if (media == "image/png" && detected != "png") || (media == "image/jpeg" && detected != "jpeg") {
			return ErrAttachmentRejected
		}
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return scanOfficeArchive(content, media)
	default:
		return ErrAttachmentRejected
	}
	return nil
}

func scanOfficeArchive(content []byte, media string) error {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(archive.File) == 0 || len(archive.File) > maxArchiveEntries {
		return ErrAttachmentRejected
	}
	var expanded uint64
	required := "word/document.xml"
	if strings.Contains(media, "spreadsheet") {
		required = "xl/workbook.xml"
	}
	foundContentTypes, foundRequired := false, false
	for _, item := range archive.File {
		name := strings.ToLower(strings.ReplaceAll(item.Name, "\\", "/"))
		if strings.HasPrefix(name, "/") || strings.Contains(name, "../") || item.FileInfo().Mode()&os.ModeSymlink != 0 {
			return ErrAttachmentRejected
		}
		if item.UncompressedSize64 > maxArchiveExpanded-expanded {
			return ErrAttachmentRejected
		}
		expanded += item.UncompressedSize64
		if (item.CompressedSize64 == 0 && item.UncompressedSize64 > 0) || (item.CompressedSize64 > 0 && item.UncompressedSize64/item.CompressedSize64 > maxArchiveRatio) {
			return ErrAttachmentRejected
		}
		if name == "[content_types].xml" {
			foundContentTypes = true
		}
		if name == required {
			foundRequired = true
		}
		if strings.Contains(name, "vbaproject.bin") || strings.Contains(name, "/activex/") || strings.Contains(name, "/embeddings/") || strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".dll") {
			return ErrAttachmentRejected
		}
		if strings.HasSuffix(name, ".rels") {
			reader, openErr := item.Open()
			if openErr != nil {
				return ErrAttachmentRejected
			}
			value, readErr := io.ReadAll(io.LimitReader(reader, 2<<20))
			reader.Close()
			if readErr != nil || bytes.Contains(bytes.ToLower(value), []byte("targetmode=\"external\"")) || bytes.Contains(bytes.ToLower(value), []byte("targetmode='external'")) {
				return ErrAttachmentRejected
			}
		}
	}
	if !foundContentTypes || !foundRequired {
		return ErrAttachmentRejected
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
