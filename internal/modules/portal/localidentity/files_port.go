package localidentity

import (
	"context"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"io"
)

type LocalReportFiles interface {
	Upload(context.Context, uint64, string, string, io.Reader) (report.FileDescriptor, error)
}
