package portalbootstrap

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type localIdentityFiles struct{ client *filegatewayclient.Client }

// NewLocalIdentityFiles 绑定同一门户应用的上传及精确报告源读取身份。
// 参数：upload 为真实网关客户端，两个ApplicationID须来自同一门户应用注册。
// 返回值：真实文件适配器；缺依赖或应用身份不一致时失败。
func NewLocalIdentityFiles(upload *filegatewayclient.Client, uploadApplicationID, sourceApplicationID string) (localidentity.LocalReportFiles, error) {
	if upload == nil || uploadApplicationID == "" || uploadApplicationID != strings.TrimSpace(uploadApplicationID) || uploadApplicationID != sourceApplicationID {
		return nil, localidentity.ErrUnavailable
	}
	return &localIdentityFiles{client: upload}, nil
}

// Upload 通过网关v2提交精确请求绑定，不把受理回执当作门户报告发布。
// 参数：ctx为真实超管，requestID/op为持久请求及操作号，name/content为原PDF。
// 返回值：明确PORTAL_LOCAL的不可变描述符；扫描与vault由既有工作器继续核实。
func (f *localIdentityFiles) Upload(ctx context.Context, requestID uint64, op, name string, content io.Reader) (report.FileDescriptor, error) {
	p, ok := auth.FromContext(ctx)
	if f == nil || f.client == nil || !ok || p.UserID == "" || len(p.Roles) != 1 || p.Roles[0] != "portal_super_admin" || !p.HasPermission(localidentity.PermissionReportManage) || requestID == 0 || op == "" || content == nil || filepath.Base(name) != name || !strings.EqualFold(filepath.Ext(name), ".pdf") {
		return report.FileDescriptor{}, localidentity.ErrInvalid
	}
	receipt, err := f.client.UploadV2(ctx, filegatewayclient.V2UploadInput{RequestID: "portal-local-v1:" + op, Purpose: "portal.local_report", Classification: "CONFIDENTIAL", Name: name, MediaType: "application/pdf", ResourceType: "PORTAL_REPORT_REQUEST", ResourceID: strconv.FormatUint(requestID, 10), BindingType: "REPORT", DisplayName: name, Content: io.LimitReader(content, (20<<20)+1)})
	if err != nil {
		return report.FileDescriptor{}, err
	}
	if receipt.FileID == "" || receipt.SizeBytes == 0 || receipt.SizeBytes > 20<<20 || len(receipt.SHA256) != 64 {
		return report.FileDescriptor{}, localidentity.ErrUnavailable
	}
	return report.FileDescriptor{SourceType: "PORTAL_LOCAL", ObjectRef: receipt.FileID, FileName: name, MIME: "application/pdf", FileHash: receipt.SHA256, Size: int64(receipt.SizeBytes)}, nil
}
