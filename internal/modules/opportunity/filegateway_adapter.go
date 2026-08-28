package opportunity

import (
	"context"
	"io"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegateway"
)

// LocalFileGatewayStore 将商机附件对象存储适配到统一本地文件网关。
// 旧 AttachmentObjectStore 接口保持不变，业务服务仍负责授权、摘要和扫描状态机。
type LocalFileGatewayStore struct{ client *filegateway.LocalClient }

// NewLocalFileGatewayStore 创建商机附件的本地文件网关适配器。
func NewLocalFileGatewayStore(client *filegateway.LocalClient) *LocalFileGatewayStore {
	return &LocalFileGatewayStore{client: client}
}

// Available 返回底层文件网关是否可用。
func (s *LocalFileGatewayStore) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

// CreateUpload 创建短时内部上传授权。
func (s *LocalFileGatewayStore) CreateUpload(ctx context.Context, key, media string, size uint64, digest, name string) (AttachmentUploadGrant, error) {
	grant, err := s.client.CreateUpload(ctx, key, media, size, digest, name)
	return AttachmentUploadGrant{URL: grant.URL, ExpiresAt: grant.ExpiresAt}, err
}

// PutVerified 把受控 content PUT 透传到本地网关；缺少该方法时适配器会被误判为
// “浏览器直传”，并返回浏览器无法访问的 internal:// 地址。
func (s *LocalFileGatewayStore) PutVerified(ctx context.Context, key string, body io.Reader, size uint64, digest, media string) error {
	if !s.Available() {
		return ErrAttachmentUnavailable
	}
	return s.client.PutVerified(ctx, key, body, size, digest, media)
}

// Finalize 将临时附件转为不可变对象，并返回摘要版本。
func (s *LocalFileGatewayStore) Finalize(ctx context.Context, key string) (AttachmentObjectMetadata, error) {
	value, err := s.client.Finalize(ctx, key)
	return AttachmentObjectMetadata{ObjectVersion: value.ObjectVersion, SizeBytes: value.SizeBytes, MIMEType: value.MIMEType, SHA256: value.SHA256}, err
}

// OpenVerified 按版本、摘要和大小打开附件，防止扫描后对象内容被替换。
func (s *LocalFileGatewayStore) OpenVerified(ctx context.Context, key, version, digest string, size uint64) (io.ReadCloser, error) {
	return s.client.OpenVerified(ctx, key, version, digest, size)
}

var _ AttachmentObjectStore = (*LocalFileGatewayStore)(nil)
var _ InternalAttachmentContentStore = (*LocalFileGatewayStore)(nil)
