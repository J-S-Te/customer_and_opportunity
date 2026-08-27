package customer

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/clamav"
)

// ClamAVImportScanner 在解析导入工作簿前执行 ClamAV 引擎扫描。它同时保留
// 最小的结构预检（必须是可读的压缩包或常见表格容器），引擎不可用时返回
// 依赖不可用错误，绝不把未完成扫描误报为安全。
type ClamAVImportScanner struct {
	client   *clamav.Client
	deadline time.Duration
}

// NewClamAVImportScanner 构造导入文件扫描器。
func NewClamAVImportScanner(client *clamav.Client, deadline time.Duration) (*ClamAVImportScanner, error) {
	if client == nil {
		return nil, errors.New("clamav import scanner requires a clamav client")
	}
	if deadline <= 0 {
		deadline = 60 * time.Second
	}
	return &ClamAVImportScanner{client: client, deadline: deadline}, nil
}

// Scan 判定导入文件是否安全：nil 安全；ErrImportFileUnsafe 明确有害；
// 其他错误表示扫描未完成。
func (s *ClamAVImportScanner) Scan(ctx context.Context, file []byte) error {
	if s == nil || s.client == nil {
		return ErrImportScannerUnavailable
	}
	// 结构预检：xlsx 是 ZIP 容器，xls/csv 不再支持导入；这里的目的是在
	// 进入引擎前拒绝明显非表格内容，真正的恶意判定交给 ClamAV。
	if len(file) == 0 || len(file) > importMaxFileBytes {
		return ErrImportInvalidFile
	}
	if !bytes.HasPrefix(file, []byte("PK\x03\x04")) {
		return ErrImportInvalidFile
	}
	scanCtx, cancel := context.WithTimeout(ctx, s.deadline)
	defer cancel()
	if err := s.client.ScanBytes(scanCtx, file); err != nil {
		if errors.Is(err, clamav.ErrInfected) {
			return ErrImportFileUnsafe
		}
		return ErrImportScannerUnavailable
	}
	return nil
}
