package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
)

// AttachmentGatewayMode 控制商机附件从既有存储向独立 File Gateway 的渐进迁移。
type AttachmentGatewayMode string

const (
	AttachmentGatewayLegacy   AttachmentGatewayMode = "legacy"
	AttachmentGatewayDual     AttachmentGatewayMode = "dual"
	AttachmentGatewayRequired AttachmentGatewayMode = "required"
)

// AttachmentGatewayMigrationInput 只包含已经通过商机、租户、操作者和内容摘要校验的附件。
type AttachmentGatewayMigrationInput struct {
	TenantID, AttachmentID, FileName, MIMEType, SHA256, IdempotencyKey string
	OpportunityID                                                      uint64
	Content                                                            []byte
}

// AttachmentGatewayMigration 是附件服务可选的迁移边界；legacy 模式不注入实现。
type AttachmentGatewayMigration interface {
	Migrate(context.Context, AttachmentGatewayMigrationInput) error
	Required() bool
}

// HTTPAttachmentGatewayMigration 使用 CRM 自己的机器凭据上传并绑定附件，不替换现有本地/S3 路径。
type HTTPAttachmentGatewayMigration struct {
	client        *filegatewayclient.Client
	applicationID string
	mode          AttachmentGatewayMode
}

// NewHTTPAttachmentGatewayMigration 创建 dual/required 迁移适配器。
func NewHTTPAttachmentGatewayMigration(client *filegatewayclient.Client, applicationID string, mode AttachmentGatewayMode) (*HTTPAttachmentGatewayMigration, error) {
	applicationID = strings.TrimSpace(applicationID)
	if client == nil || applicationID == "" || mode != AttachmentGatewayDual && mode != AttachmentGatewayRequired {
		return nil, errors.New("attachment file gateway migration configuration is invalid")
	}
	return &HTTPAttachmentGatewayMigration{client: client, applicationID: applicationID, mode: mode}, nil
}

// Required 表示网关写入失败时上传接口必须失败关闭。
func (migration *HTTPAttachmentGatewayMigration) Required() bool {
	return migration != nil && migration.mode == AttachmentGatewayRequired
}

// Migrate 以稳定请求键上传同一内容，并绑定到已经通过权限校验的商机附件资源。
func (migration *HTTPAttachmentGatewayMigration) Migrate(ctx context.Context, input AttachmentGatewayMigrationInput) error {
	if migration == nil || migration.client == nil || strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.AttachmentID) == "" || len(input.Content) == 0 {
		return errors.New("attachment file gateway migration input is invalid")
	}
	uploadKey := attachmentGatewayRequestKey("upload", input)
	fileID, err := migration.client.Upload(ctx, uploadKey, migration.applicationID, "CONFIDENTIAL", input.FileName, input.MIMEType, bytes.NewReader(input.Content))
	if err != nil {
		return fmt.Errorf("upload attachment to file gateway: %w", err)
	}
	bindKey := attachmentGatewayRequestKey("bind", input)
	if err = migration.client.Bind(ctx, bindKey, migration.applicationID, fileID, "opportunity_attachment", input.AttachmentID, "PRIMARY", input.FileName); err != nil {
		return fmt.Errorf("bind attachment in file gateway: %w", err)
	}
	return nil
}

func attachmentGatewayRequestKey(operation string, input AttachmentGatewayMigrationInput) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{operation, input.TenantID, uintString(input.OpportunityID), input.AttachmentID, input.IdempotencyKey, input.SHA256}, "\x00")))
	return "crm-attachment-" + operation + ":" + hex.EncodeToString(sum[:])
}
