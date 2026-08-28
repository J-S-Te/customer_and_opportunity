package report

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegateway"
)

// LocalFileGatewayReader 将报告下载读取边界适配到本地文件网关。
// 报告记录中的对象键仍由 DescriptorProtector 加密保存，下载时先解密再进行版本校验。
type LocalFileGatewayReader struct {
	client    *filegateway.LocalClient
	protector DescriptorProtector
}

// NewLocalFileGatewayReader 创建本地报告文件读取适配器。
func NewLocalFileGatewayReader(client *filegateway.LocalClient, protector DescriptorProtector) *LocalFileGatewayReader {
	return &LocalFileGatewayReader{client: client, protector: protector}
}

// Available 返回网关和对象键解密器是否就绪。
func (r *LocalFileGatewayReader) Available() bool {
	return r != nil && r.client != nil && r.client.Available() && r.protector != nil
}

// OpenVerified 解密对象键并按报告数据库固化的版本、摘要和大小打开文件。
func (r *LocalFileGatewayReader) OpenVerified(ctx context.Context, value *File) (PreparedDownload, error) {
	if !r.Available() || value == nil || len(value.ObjectKeyCipher) == 0 {
		return PreparedDownload{}, ErrFileUnavailable
	}
	key, err := r.protector.Decrypt(ctx, value.ObjectKeyCipher)
	if err != nil {
		return PreparedDownload{}, ErrFileUnavailable
	}
	reader, err := r.client.OpenVerified(ctx, string(key), value.ObjectVersion, value.FileHash, uint64(value.Size))
	if err != nil {
		return PreparedDownload{}, ErrFileUnavailable
	}
	return PreparedDownload{Reader: reader, FileName: value.FileName, MIME: value.MIME, Size: value.Size, FileHash: value.FileHash}, nil
}

var _ FileReader = (*LocalFileGatewayReader)(nil)
