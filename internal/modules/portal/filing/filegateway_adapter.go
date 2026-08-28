package filing

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegateway"
)

// LocalFileGatewayStore 将备案材料存储接口适配到本地文件网关。
// 适配器只转换类型，不绕过 MaterialService 的租户、摘要和扫描校验。
type LocalFileGatewayStore struct{ client *filegateway.LocalClient }

// NewLocalFileGatewayStore 创建备案材料的本地文件网关适配器。
func NewLocalFileGatewayStore(client *filegateway.LocalClient) *LocalFileGatewayStore {
	return &LocalFileGatewayStore{client: client}
}

// Available 返回底层本地文件网关是否可用。
func (s *LocalFileGatewayStore) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

// CreateUpload 创建短时内部上传授权。
func (s *LocalFileGatewayStore) CreateUpload(ctx context.Context, key, media string, size uint64, digest, name string) (string, time.Time, error) {
	grant, err := s.client.CreateUpload(ctx, key, media, size, digest, name)
	return grant.URL, grant.ExpiresAt, err
}

// PutVerified 通过本地文件网关受控写入材料内容；任何元数据或摘要不一致都映射为稳定的
// 业务校验错误，路由层不会把内部文件路径或底层错误返回给浏览器。
func (s *LocalFileGatewayStore) PutVerified(ctx context.Context, key string, body io.Reader, size uint64, digest, media string) error {
	err := s.client.PutVerified(ctx, key, body, size, digest, media)
	if errors.Is(err, filegateway.ErrInvalid) {
		return ErrMaterialContentInvalid
	}
	return err
}

// Finalize 将已校验的临时材料转为不可变对象。
func (s *LocalFileGatewayStore) Finalize(ctx context.Context, key string) (MaterialObjectMetadata, error) {
	value, err := s.client.Finalize(ctx, key)
	return MaterialObjectMetadata{ObjectVersion: value.ObjectVersion, SizeBytes: value.SizeBytes, MIMEType: value.MIMEType, SHA256: value.SHA256}, err
}

// OpenVerified 按版本和摘要读取已完成的备案材料。
func (s *LocalFileGatewayStore) OpenVerified(ctx context.Context, key, version, digest string, size uint64) (io.ReadCloser, error) {
	return s.client.OpenVerified(ctx, key, version, digest, size)
}

var _ InternalMaterialContentStore = (*LocalFileGatewayStore)(nil)
