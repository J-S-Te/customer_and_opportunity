package reportsecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

func pdfFixture(t *testing.T) []byte {
	t.Helper()
	old := model.ConfigPath
	model.ConfigPath = "disable"
	t.Cleanup(func() { model.ConfigPath = old })
	doc := map[string]any{"paper": "A4P", "pages": map[string]any{"1": map[string]any{"content": map[string]any{}}}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := api.Create(nil, bytes.NewReader(raw), &out, model.NewDefaultConfiguration()); err != nil {
		t.Fatal(err)
	}
	rawPDF := out.Bytes()
	conf := &model.Configuration{Reader15: true, ValidationMode: model.ValidationStrict, DecodeAllStreams: true}
	if err := api.Validate(bytes.NewReader(rawPDF), conf); err != nil {
		t.Fatalf("fixture PDF strict validation: %v", err)
	}
	return rawPDF
}
func descriptor(raw []byte) report.FileDescriptor {
	hash := sha256.Sum256(raw)
	return report.FileDescriptor{ObjectRef: "file-1", FileName: "report.pdf", MIME: "application/pdf", FileHash: hex.EncodeToString(hash[:]), Size: int64(len(raw))}
}
func storeFixture(t *testing.T) (*EncryptedStore, report.IngestJob) {
	t.Helper()
	protector, err := report.NewAESDescriptorProtector(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewEncryptedStore(t.TempDir(), bytes.Repeat([]byte{1}, 32), "tenant-1", protector)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, report.IngestJob{TenantID: "tenant-1", CustomerID: 2, RequestID: 3, ReportRevision: 1, EventID: "event-1"}
}
func fileFromReceipt(job report.IngestJob, d report.FileDescriptor, r report.IngestResult) *report.File {
	return &report.File{Model: database.Model{TenantID: job.TenantID}, RequestID: job.RequestID, ReportRevision: job.ReportRevision, ObjectKeyCipher: r.ObjectKeyCipher, ObjectVersion: r.ObjectVersion, FileName: d.FileName, MIME: d.MIME, Size: d.Size, FileHash: d.FileHash, EncryptionKeyRef: r.EncryptionKeyRef, EncryptionAlgorithm: r.EncryptionAlgorithm, ScanStatus: r.ScanStatus, ScanReference: r.ScanReference, ScannedAt: &r.ScannedAt, WatermarkStatus: r.WatermarkStatus}
}
func TestEncryptedReportIngestRestartIdempotencyScopeAndIntegrity(t *testing.T) {
	raw := pdfFixture(t)
	d := descriptor(raw)
	store, job := storeFixture(t)
	receipt, err := store.Persist(context.Background(), job, d, raw)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ScanStatus != "CLEAN" || !strings.HasPrefix(receipt.ScanReference, scannerVersion+":") || receipt.EncryptionAlgorithm != "AES-256-GCM" {
		t.Fatal("unproven security evidence")
	}
	again, err := store.Persist(context.Background(), job, d, raw)
	if err != nil || !again.ScannedAt.Equal(receipt.ScannedAt) {
		t.Fatal("idempotency lost", err)
	}
	ciphertext, err := store.root.ReadFile(manifestID(job) + ".report")
	if err != nil || bytes.Contains(ciphertext, []byte("%PDF-")) {
		t.Fatal("plaintext report persisted", err)
	}
	root := store.root.Name()
	restarted, err := NewEncryptedStore(root, bytes.Repeat([]byte{1}, 32), "tenant-1", store.protector)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	f := fileFromReceipt(job, d, receipt)
	content, err := restarted.OpenVerified(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(content.Reader)
	closeErr := content.Reader.Close()
	if err != nil || closeErr != nil || !bytes.Equal(got, raw) {
		t.Fatal("restart read corrupted", err, closeErr)
	}
	for _, change := range []func(*report.File){func(f *report.File) { f.TenantID = "other" }, func(f *report.File) { f.RequestID++ }, func(f *report.File) { f.ReportRevision++ }, func(f *report.File) { f.ScanReference = "fake" }, func(f *report.File) { f.EncryptionKeyRef = "fake" }} {
		bad := *f
		change(&bad)
		if _, err := store.OpenVerified(context.Background(), &bad); err == nil {
			t.Fatal("unbound report read accepted")
		}
	}
	different := d
	different.FileName = "replaced.pdf"
	if _, err := store.Persist(context.Background(), job, different, raw); err == nil {
		t.Fatal("same event changed descriptor")
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if err := store.root.WriteFile(manifestID(job)+".report", ciphertext, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenVerified(context.Background(), f); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
func TestReportScanRejectsActiveMalformedAndFalseHashes(t *testing.T) {
	raw := pdfFixture(t)
	for _, bad := range [][]byte{[]byte("%PDF-not-a-document"), append(append([]byte(nil), raw...), []byte("/JavaScript")...), append(append([]byte(nil), raw...), []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")...)} {
		if err := ValidatePDF(context.Background(), bad, descriptor(bad)); err == nil {
			t.Fatal("unsafe PDF accepted")
		}
	}
	d := descriptor(raw)
	d.FileHash = strings.Repeat("0", 64)
	if ValidatePDF(context.Background(), raw, d) == nil {
		t.Fatal("false hash accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(ValidatePDF(ctx, raw, descriptor(raw)), context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

type gatewayFixture struct {
	data          []byte
	input         filegatewayclient.PortalReportSourceInput
	trace, fileID string
	opens         int
	err           error
	version       string
}

func (g *gatewayFixture) OpenPortalReportFile(_ context.Context, trace, fileID string, input filegatewayclient.PortalReportSourceInput) (filegatewayclient.PortalReportFile, error) {
	g.opens++
	g.input, g.trace, g.fileID = input, trace, fileID
	if g.err != nil {
		return filegatewayclient.PortalReportFile{}, g.err
	}
	return filegatewayclient.PortalReportFile{VersionID: g.version, SHA256: input.ExpectedSHA256, MIME: input.ExpectedMIME, Size: input.ExpectedSize, Content: io.NopCloser(bytes.NewReader(g.data))}, nil
}
func TestGatewayIngestRequiresExactBusinessBindingAndTrustedVersion(t *testing.T) {
	raw := pdfFixture(t)
	d := descriptor(raw)
	store, job := storeFixture(t)
	source := &gatewayFixture{data: raw, version: "version-actual"}
	ingest, err := NewGatewayIngestor(source, store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ingest.IngestForJob(context.Background(), job, d)
	if err != nil {
		t.Fatal(err)
	}
	if source.input.RequestID != job.RequestID || source.input.ExpectedSHA256 != d.FileHash || source.trace != job.EventID || source.fileID != d.ObjectRef || receipt.ObjectVersion != source.version {
		t.Fatal("source not bound to report scope/version")
	}
	before := source.opens
	job.TenantID = "other"
	if _, err := ingest.IngestForJob(context.Background(), job, d); err == nil || source.opens != before {
		t.Fatal("wrong tenant opened")
	}
	if _, err := ingest.Ingest(context.Background(), "event-1", d); err == nil {
		t.Fatal("unscoped ingest accepted")
	}
	job.TenantID = "tenant-1"
	source.err = ErrUnavailable
	if _, err := ingest.IngestForJob(context.Background(), job, d); err == nil {
		t.Fatal("unavailable source accepted")
	}
	source.err = nil
	source.version = ""
	if _, err := ingest.IngestForJob(context.Background(), job, d); err == nil {
		t.Fatal("missing immutable version accepted")
	}
}

type riskFixture struct {
	baseline report.DownloadMetadata
	count    int64
	err      error
}

func (r riskFixture) Issuance(context.Context, report.Actor, *report.Grant) (report.DownloadMetadata, error) {
	return r.baseline, r.err
}
func (r riskFixture) RecentSuccesses(context.Context, report.Actor, uint64, time.Time) (int64, error) {
	return r.count, r.err
}
func TestAuditRiskPolicyEvaluatesNetworkDeviceRateAndFailsClosed(t *testing.T) {
	actor := report.Actor{TenantID: "tenant-1", CustomerID: 2, AccountID: "subject-1"}
	request := &report.Request{ActorModel: report.ActorModel{TenantID: actor.TenantID}, CustomerID: 2}
	request.ID = 3
	grant := &report.Grant{ActorModel: report.ActorModel{TenantID: actor.TenantID}, CustomerID: 2, RequestID: 3, AccountID: actor.AccountID}
	baseline := report.DownloadMetadata{IPHash: strings.Repeat("a", 64), DeviceHash: strings.Repeat("b", 64)}
	for _, tc := range []struct {
		name      string
		metadata  report.DownloadMetadata
		count     int64
		err       error
		reason    string
		wantError bool
	}{
		{name: "normal", metadata: baseline}, {name: "device", metadata: report.DownloadMetadata{IPHash: baseline.IPHash, DeviceHash: strings.Repeat("c", 64)}, reason: "DOWNLOAD_DEVICE_CHANGED"},
		{name: "network", metadata: report.DownloadMetadata{IPHash: strings.Repeat("d", 64), DeviceHash: baseline.DeviceHash}, reason: "DOWNLOAD_NETWORK_CHANGED"}, {name: "rate", metadata: baseline, count: 5, reason: "DOWNLOAD_RATE_EXCEEDED"},
		{name: "storage unavailable", metadata: baseline, err: ErrUnavailable, wantError: true}, {name: "missing hashes", wantError: true}} {
		t.Run(tc.name, func(t *testing.T) {
			policy, _ := NewAuditRiskPolicy(riskFixture{baseline: baseline, count: tc.count, err: tc.err}, 5*time.Minute, 5)
			freeze, reason, err := policy.Evaluate(context.Background(), actor, request, grant, tc.metadata)
			if (err != nil) != tc.wantError || reason != tc.reason || freeze != (tc.reason != "") {
				t.Fatal(freeze, reason, err)
			}
		})
	}
}

type numberFixture struct{}

func (numberFixture) RequestNumber(context.Context, report.WatermarkContext) (string, error) {
	return "REPORT-123", nil
}
func TestRealChineseWatermarkRequiresAuthoritativeScopedHTTPIdentity(t *testing.T) {
	raw := pdfFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-Integration-Nonce") == "" || r.Header.Get("X-Integration-Timestamp") == "" || r.URL.Query().Get("account_id") != "subject-1" {
			t.Error("unbound identity request")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "OK", "data": portalreportreference.Identity{TenantID: "tenant-1", CustomerID: 2, AccountID: "subject-1", CustomerName: "测试客户单位", AccountMasked: "13*******90"}})
	}))
	defer server.Close()
	identity := &IdentityHTTPClient{Endpoint: server.URL, Client: server.Client(), Token: func(context.Context) (string, error) { return "fixture-token", nil }}
	copied := cjkFontFixture(t)
	watermark, err := NewPDFWatermarker(t.TempDir(), copied, identity, numberFixture{})
	if err != nil {
		t.Fatal(err)
	}
	stamp := report.WatermarkContext{TenantID: "tenant-1", CustomerID: 2, RequestID: 3, AccountID: "subject-1", TrackingCode: "tracking-test-unique", DownloadedAt: time.Now().UTC()}
	result, err := watermark.Apply(context.Background(), raw, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(raw, result) || api.Validate(bytes.NewReader(result), watermark.conf) != nil {
		t.Fatal("not a valid stamped PDF")
	}
	pages, err := api.PageCount(bytes.NewReader(result), watermark.conf)
	if err != nil || pages != 1 {
		t.Fatal("changed report pagination", pages, err)
	}
	if output := os.Getenv("UIP_P1_WATERMARK_SAMPLE"); output != "" {
		if filepath.Base(output) != "portal-watermark-sample.pdf" {
			t.Fatal("unexpected QA artifact name")
		}
		if err := os.WriteFile(output, result, 0600); err != nil {
			t.Fatal(err)
		}
	}
	stamp.CustomerID = 999
	if _, err := watermark.Apply(context.Background(), raw, stamp); err == nil {
		t.Fatal("cross-customer identity accepted")
	}
}

func cjkFontFixture(t *testing.T) string {
	t.Helper()
	source := os.Getenv("UIP_TEST_CJK_FONT_PATH")
	name := os.Getenv("UIP_TEST_CJK_FONT_NAME")
	if source == "" {
		source = "../../assets/fonts/UIPPortalSans-Regular.ttf"
		if name == "" {
			name = "UIPPortalSans-Regular"
		}
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	}
	if filepath.Base(name) != name || name == "" {
		t.Fatal("test font name must be its PostScript name")
	}
	font, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("real CJK test asset required: set UIP_TEST_CJK_FONT_PATH and matching UIP_TEST_CJK_FONT_NAME: %v", err)
	}
	copied := filepath.Join(t.TempDir(), name+".ttf")
	if err := os.WriteFile(copied, font, 0600); err != nil {
		t.Fatal(err)
	}
	return copied
}
