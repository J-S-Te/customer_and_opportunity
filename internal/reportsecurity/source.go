package reportsecurity

import (
	"context"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"io"
	"regexp"
	"strings"
)

// 委托源端点校验源 project.report 及该精确的
// Portal 请求绑定；工作器绝不持有 Project 应用密钥。
const SourceResourceType = "PORTAL_REPORT_REQUEST"

var fileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type GatewaySource interface {
	OpenPortalReportFile(context.Context, string, string, filegatewayclient.PortalReportSourceInput) (filegatewayclient.PortalReportFile, error)
}
type GatewayIngestor struct {
	source GatewaySource
	store  *EncryptedStore
}

func NewGatewayIngestor(source GatewaySource, store *EncryptedStore) (*GatewayIngestor, error) {
	if source == nil || store == nil || !store.Available() {
		return nil, ErrUnavailable
	}
	return &GatewayIngestor{source: source, store: store}, nil
}
func (g *GatewayIngestor) Ingest(context.Context, string, report.FileDescriptor) (report.IngestResult, error) {
	return report.IngestResult{}, ErrInvalid
}
func (g *GatewayIngestor) IngestForJob(ctx context.Context, job report.IngestJob, d report.FileDescriptor) (report.IngestResult, error) {
	if g == nil || g.source == nil || g.store == nil || job.TenantID != g.store.tenant || job.CustomerID == 0 || job.RequestID == 0 || job.ReportRevision == 0 || job.EventID == "" || !fileIDPattern.MatchString(d.ObjectRef) || d.Size <= 0 || d.Size > maxPDFBytes || d.MIME != "application/pdf" {
		return report.IngestResult{}, ErrInvalid
	}
	source, err := g.source.OpenPortalReportFile(ctx, job.EventID, d.ObjectRef, filegatewayclient.PortalReportSourceInput{RequestID: job.RequestID, ExpectedSHA256: d.FileHash, ExpectedSize: d.Size, ExpectedMIME: d.MIME, ExpectedName: d.FileName})
	if err != nil {
		return report.IngestResult{}, ErrUnavailable
	}
	if source.Content == nil {
		return report.IngestResult{}, ErrInvalid
	}
	content, readErr := io.ReadAll(io.LimitReader(source.Content, d.Size+1))
	closeErr := source.Content.Close()
	if readErr != nil || closeErr != nil || int64(len(content)) != d.Size || source.Size != d.Size || source.MIME != d.MIME || !strings.EqualFold(source.SHA256, d.FileHash) || !safeText(source.VersionID, 256) {
		return report.IngestResult{}, ErrInvalid
	}
	return g.store.PersistSource(ctx, job, d, content, source.VersionID)
}
