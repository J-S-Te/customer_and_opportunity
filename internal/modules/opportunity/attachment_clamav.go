package opportunity

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/clamav"
)

const clamavScannerVersion = "clamav-v1"

// ClamAVAttachmentScanner 在进程内静态内容校验之上叠加 ClamAV 引擎扫描：
// 两道检查都必须通过才返回 CLEAN，任何一道判定恶意即 REJECTED，引擎
// 不可用则扫描失败（依赖不可用，绝不放行）。扫描引用格式与本地扫描器
// 一致（<version>:<status>:<digest>），ImmediateStatus 可同步解析。
type ClamAVAttachmentScanner struct {
	store    AttachmentObjectStore
	client   *clamav.Client
	deadline time.Duration
}

// NewClamAVAttachmentScanner 构造 ClamAV 附件扫描器。store 用于读取待扫描
// 对象内容（通常是 S3 或本地对象存储）；deadline 是单次 clamd 调用的上限。
func NewClamAVAttachmentScanner(store AttachmentObjectStore, client *clamav.Client, deadline time.Duration) (*ClamAVAttachmentScanner, error) {
	if store == nil || client == nil {
		return nil, errors.New("clamav attachment scanner requires a content store and a clamav client")
	}
	if deadline <= 0 {
		deadline = 90 * time.Second
	}
	return &ClamAVAttachmentScanner{store: store, client: client, deadline: deadline}, nil
}

func (s *ClamAVAttachmentScanner) Available() bool {
	return s != nil && s.store != nil && s.store.Available() && s.client != nil
}

func (s *ClamAVAttachmentScanner) Submit(ctx context.Context, _ string, key, version, digest string, size uint64, media string) (string, error) {
	if !s.Available() {
		return "", ErrAttachmentUnavailable
	}
	reader, err := s.store.OpenVerified(ctx, key, version, digest, size)
	if err != nil {
		return "", err
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, int64(size)+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || uint64(len(content)) != size {
		return "", ErrAttachmentInvalid
	}
	// 第一道：进程内静态内容校验（可执行头、PDF 活性内容、图片尺寸、OOXML 结构）。
	if scanAttachmentBytes(content, media) != nil {
		return clamavScannerVersion + ":" + strings.ToLower(AttachmentRejected) + ":" + strings.ToLower(digest), nil
	}
	// 第二道：ClamAV 引擎扫描。引擎不可用返回错误，由上层按失败关闭处理。
	scanCtx, cancel := context.WithTimeout(ctx, s.deadline)
	defer cancel()
	if err = s.client.ScanBytes(scanCtx, content); err != nil {
		if errors.Is(err, clamav.ErrInfected) {
			return clamavScannerVersion + ":" + strings.ToLower(AttachmentRejected) + ":" + strings.ToLower(digest), nil
		}
		return "", err
	}
	return clamavScannerVersion + ":" + strings.ToLower(AttachmentClean) + ":" + strings.ToLower(digest), nil
}

func (*ClamAVAttachmentScanner) ImmediateStatus(reference string) (string, bool) {
	parts := strings.Split(reference, ":")
	if len(parts) != 3 || parts[0] != clamavScannerVersion {
		return "", false
	}
	status := strings.ToUpper(parts[1])
	return status, status == AttachmentClean || status == AttachmentRejected
}
