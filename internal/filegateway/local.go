// Package filegateway 提供 CRM 与客户门户共用的本地文件网关实现。
//
// 网关只接受服务端生成的对象键，上传完成前使用临时后缀，完成后以摘要
// 和不可变版本校验读取。它不是公网静态文件目录，也不会把客户端文件名
// 拼接进宿主机路径。
package filegateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const uploadTTL = 10 * time.Minute

var (
	ErrUnavailable = errors.New("file gateway is unavailable")
	ErrInvalid     = errors.New("file gateway object is invalid")
)

// UploadGrant 描述一次受服务端控制的上传授权。
type UploadGrant struct {
	URL       string
	ExpiresAt time.Time
}

// Metadata 是完成对象上传后固化的完整性元数据。
type Metadata struct {
	ObjectVersion string
	SizeBytes     uint64
	MIMEType      string
	SHA256        string
}

type metadata struct {
	SizeBytes uint64 `json:"size_bytes"`
	MIMEType  string `json:"mime_type"`
	SHA256    string `json:"sha256"`
}

// LocalClient 是仅供应用进程访问的本地文件网关客户端。
type LocalClient struct{ root string }

// NewLocalClient 创建本地文件网关，并将根目录权限收紧为仅属主可访问。
func NewLocalClient(root string) (*LocalClient, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("file gateway root must be an absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create file gateway root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect file gateway root: %w", err)
	}
	return &LocalClient{root: root}, nil
}

// Available 返回网关是否已完成初始化。
func (c *LocalClient) Available() bool { return c != nil && c.root != "" }

// CreateUpload 创建一次短时上传授权；实际上传仍必须通过 PutVerified 完成。
func (c *LocalClient) CreateUpload(context.Context, string, string, uint64, string, string) (UploadGrant, error) {
	if !c.Available() {
		return UploadGrant{}, ErrUnavailable
	}
	return UploadGrant{URL: "internal://file-gateway-upload", ExpiresAt: time.Now().UTC().Add(uploadTTL)}, nil
}

func (c *LocalClient) path(key, suffix string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrInvalid
	}
	path := filepath.Join(c.root, clean) + suffix
	rel, err := filepath.Rel(c.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalid
	}
	return path, nil
}

// PutVerified 以临时文件原子写入对象，并校验大小、媒体类型和 SHA-256 摘要。
func (c *LocalClient) PutVerified(_ context.Context, key string, body io.Reader, size uint64, digest, media string) error {
	path, err := c.path(key, ".upload")
	if err != nil || size == 0 || strings.TrimSpace(digest) == "" || strings.TrimSpace(media) == "" {
		return ErrInvalid
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".incoming-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer os.Remove(tmpName + ".meta")
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(body, int64(size)+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || written != int64(size) || !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), digest) {
		return ErrInvalid
	}
	meta, _ := json.Marshal(metadata{SizeBytes: size, MIMEType: media, SHA256: strings.ToLower(digest)})
	if err = os.WriteFile(tmpName+".meta", meta, 0o600); err != nil {
		return err
	}
	// 硬链接发布使用目标不存在语义，不像 Rename 那样覆盖既有上传。并发或重试命中
	// 已有对象时，仅当其完整元数据一致才视为幂等成功。
	if err = os.Link(tmpName, path); err != nil {
		if existing, readErr := c.readMetadata(path); readErr == nil && existing.SizeBytes == size && existing.MIMEType == media && strings.EqualFold(existing.SHA256, digest) {
			return nil
		}
		return ErrInvalid
	}
	if err = os.Link(tmpName+".meta", path+".meta"); err != nil {
		_ = os.Remove(path)
		return ErrInvalid
	}
	return nil
}

// Finalize 将临时对象转为只读对象，并返回以内容摘要为版本的元数据。
func (c *LocalClient) Finalize(_ context.Context, key string) (Metadata, error) {
	upload, err := c.path(key, ".upload")
	if err != nil {
		return Metadata{}, err
	}
	final, err := c.path(key, "")
	if err != nil {
		return Metadata{}, err
	}
	if _, statErr := os.Stat(final); statErr == nil {
		return c.readMetadata(final)
	}
	if err = os.Rename(upload, final); err != nil {
		return Metadata{}, err
	}
	if err = os.Rename(upload+".meta", final+".meta"); err != nil {
		_ = os.Rename(final, upload)
		return Metadata{}, err
	}
	if err = os.Chmod(final, 0o400); err != nil {
		return Metadata{}, err
	}
	_ = os.Chmod(final+".meta", 0o400)
	return c.readMetadata(final)
}

func (c *LocalClient) readMetadata(path string) (Metadata, error) {
	body, err := os.ReadFile(path + ".meta")
	if err != nil {
		return Metadata{}, err
	}
	var expected metadata
	if json.Unmarshal(body, &expected) != nil || expected.SizeBytes == 0 || expected.MIMEType == "" || len(expected.SHA256) != 64 {
		return Metadata{}, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	actual := hex.EncodeToString(hasher.Sum(nil))
	if err != nil || size != int64(expected.SizeBytes) || !strings.EqualFold(actual, expected.SHA256) {
		return Metadata{}, ErrInvalid
	}
	return Metadata{ObjectVersion: actual, SizeBytes: expected.SizeBytes, MIMEType: expected.MIMEType, SHA256: actual}, nil
}

// OpenVerified 打开已完成对象，并再次校验版本、大小和摘要，防止扫描后对象被替换。
func (c *LocalClient) OpenVerified(_ context.Context, key, version, digest string, size uint64) (io.ReadCloser, error) {
	path, err := c.path(key, "")
	if err != nil {
		return nil, err
	}
	meta, err := c.readMetadata(path)
	if err != nil || meta.ObjectVersion != version || meta.SizeBytes != size || !strings.EqualFold(meta.SHA256, digest) {
		return nil, ErrInvalid
	}
	return os.Open(path)
}
