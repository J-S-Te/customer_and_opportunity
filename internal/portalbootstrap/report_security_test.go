package portalbootstrap

import (
	"bytes"
	"context"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

func TestReportDownloadConfigRejectsPartialReusedOrMissingFontAndIdentityDependencies(t *testing.T) {
	c := validPortalConfig()
	if err := validateReportDownloadConfig(c); err != nil {
		t.Fatal(err)
	}
	c.ReportContentKey = bytes.Repeat([]byte{7}, 32)
	if err := validateReportDownloadConfig(c); err == nil {
		t.Fatal("partial report config accepted")
	}
	c.ReportStoreRoot = "/data/private-reports"
	c.ReportIdentityURL = "https://crm.example/customer-opportunity/api/v1/internal/portal-report/download-identity"
	c.ReportIdentityClientID = "portal-report-identity"
	c.ReportIdentityClientSecret = "fixture"
	c.PDFConfigRoot = "/data/pdf"
	c.CJKFontPath = "/fonts/Font-Regular.ttf"
	if err := validateReportDownloadConfig(c); err != nil {
		t.Fatal(err)
	}
	c.ReportContentKey = c.ReportIngestDescriptorKey
	if err := validateReportDownloadConfig(c); err == nil {
		t.Fatal("shared descriptor encryption key accepted")
	}
}
func TestUnconfiguredReportProvidersCannotClaimDownloadReadiness(t *testing.T) {
	c := validPortalConfig()
	protector, err := report.NewAESDescriptorProtector(c.ReportIngestDescriptorKey)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := newReportDownloadProviders(context.Background(), nil, c, protector)
	if err != nil || providers != nil {
		t.Fatal("disabled compatibility fabricated production providers", providers, err)
	}
}
