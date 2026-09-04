package customer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/safexlsx"
)

type importScannerStub struct{ err error }

func (s importScannerStub) Scan(context.Context, []byte) error { return s.err }

func TestImportRoutesRequireCustomerImportPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, "/customers/imports/preview"},
		{http.MethodGet, "/customers/imports/template"},
		{http.MethodPost, "/customers/imports/job/commit"},
		{http.MethodGet, "/customers/imports/job/errors"},
	} {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			principal := auth.Principal{TenantID: "tenant-a", UserID: "user-a", Permissions: map[string]struct{}{}}
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		})
		RegisterRoutes(router.Group(""), NewHandler(&Service{}))
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "COMMON_FORBIDDEN") {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestCustomerImportTemplateDownloadsValidWorkbook(t *testing.T) {
	contents, err := customerImportTemplateWorkbook()
	if err != nil {
		t.Fatalf("customerImportTemplateWorkbook() error = %v", err)
	}
	workbook, err := safexlsx.ParseWorkbook(contents, safexlsx.Limits{MaxArchiveBytes: importMaxFileBytes, MaxRows: importMaxDataRows + 1, MaxColumns: len(importHeaders), MaxCellRunes: 500})
	if err != nil {
		t.Fatalf("template parsing error = %v", err)
	}
	if err = validateImportHeader(workbook); err != nil {
		t.Fatalf("template header error = %v", err)
	}
	if len(workbook) != 2 || workbook[1][0].Value != customerImportTemplateExample[0] || workbook[1][9].Value != customerImportTemplateExample[9] {
		t.Fatalf("template rows = %#v", workbook)
	}
}

func TestPreviewHandlerRejectsTypeExtensionUnknownAndOversizedParts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		contentType string
		build       func(*multipart.Writer) error
	}{
		{name: "content type", contentType: "application/json"},
		{name: "extension", build: func(writer *multipart.Writer) error {
			part, _ := writer.CreateFormFile("file", "customers.csv")
			_, _ = part.Write([]byte("csv"))
			return writer.WriteField("reason", "import")
		}},
		{name: "unknown field", build: func(writer *multipart.Writer) error {
			part, _ := writer.CreateFormFile("file", "customers.xlsx")
			_, _ = part.Write([]byte("xlsx"))
			_ = writer.WriteField("reason", "import")
			return writer.WriteField("tenant_id", "other")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if test.build != nil {
				_ = test.build(writer)
			}
			_ = writer.Close()
			contentType := test.contentType
			if contentType == "" {
				contentType = writer.FormDataContentType()
			}
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/customers/imports/preview", &body)
			context.Request.Header.Set("Content-Type", contentType)
			(&Handler{}).PreviewImport(context)
			if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPreviewImportFailsClosedWithoutScannerAndMasksHelpers(t *testing.T) {
	service := &Service{imports: &GORMRepository{}, codec: profileCodec(t)}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "user-a"})
	if _, err := service.PreviewImport(ctx, []byte("PK\x03\x04"), "reason"); !errors.Is(err, ErrImportScannerUnavailable) {
		t.Fatalf("missing scanner err=%v", err)
	}
	service.scanner = importScannerStub{err: ErrImportFileUnsafe}
	if _, err := service.PreviewImport(ctx, []byte("PK\x03\x04"), "reason"); !errors.Is(err, ErrImportScanFailed) {
		t.Fatalf("scanner rejection err=%v", err)
	}
	service.scanner = importScannerStub{err: errors.New("scanner timeout")}
	if _, err := service.PreviewImport(ctx, []byte("PK\x03\x04"), "reason"); !errors.Is(err, ErrImportScannerUnavailable) {
		t.Fatalf("scanner dependency failure err=%v", err)
	}
	if got := maskCreditCode("913100001234567890"); strings.Contains(got, "1234567890") || got == "913100001234567890" {
		t.Fatalf("credit code leaked: %s", got)
	}
}

func TestImportHeaderAndRowsFailClosed(t *testing.T) {
	header := make([]safexlsx.Cell, len(importHeaders))
	for index, value := range importHeaders {
		header[index].Value = value
	}
	validData := make([]safexlsx.Cell, len(importHeaders))
	values := []string{"示例客户", "913100001234567890", "企业", "科技", "华东", "owner-a", "org-a", "张三", "13800138000", "zhang@example.com"}
	for index, value := range values {
		validData[index].Value = value
	}
	if err := validateImportHeader([][]safexlsx.Cell{header, validData}); err != nil {
		t.Fatalf("valid header rejected: %v", err)
	}
	header[0].Formula = true
	if err := validateImportHeader([][]safexlsx.Cell{header, validData}); err == nil || apperror.As(err).Code != ErrImportInvalidFile.Code {
		t.Fatalf("formula header accepted: %v", err)
	}
	header[0].Formula = false
	validData[3].Value = "=CMD()"
	validData[8].Formula = true
	parsed := parseImportRow(2, validData)
	encoded, _ := json.Marshal(parsed.preview)
	if !hasImportError(parsed.issues) || !strings.Contains(string(encoded), "138****8000") || strings.Contains(string(encoded), "13800138000") || strings.Contains(string(encoded), "zhang@example.com") {
		t.Fatalf("row not safely rejected/masked: issues=%#v dto=%s", parsed.issues, encoded)
	}
}

func TestImportRejectsNumericCustomerMasterDataPlaceholders(t *testing.T) {
	row := make([]safexlsx.Cell, len(importHeaders))
	values := []string{"1", "913100001234567890", "1", "科技", "1", "owner-a", "org-a", "张三", "13800138000", "zhang@example.com"}
	for index, value := range values {
		row[index].Value = value
	}
	parsed := parseImportRow(2, row)
	if !hasImportError(parsed.issues) {
		t.Fatalf("numeric master data placeholder accepted: %#v", parsed)
	}
}

func TestImportCSVUsesRFC4180AndNeutralizesInjection(t *testing.T) {
	var output strings.Builder
	writer := csv.NewWriter(&output)
	if err := writer.Write([]string{safeCSV("=cmd|' /C calc'!A0"), safeCSV("line,\nquoted")}); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	reader := csv.NewReader(strings.NewReader(output.String()))
	record, err := reader.Read()
	if err != nil || !strings.HasPrefix(record[0], "'=") || record[1] != "line,\nquoted" {
		t.Fatalf("record=%#v err=%v raw=%q", record, err, output.String())
	}
}

func TestImportRequestHashBindsJobAndVersion(t *testing.T) {
	base := importRequestHash("job-a", 1)
	if base == importRequestHash("job-b", 1) || base == importRequestHash("job-a", 2) {
		t.Fatal("request hash does not bind job and version")
	}
}

func TestImportJobNumberValidationRejectsPathLikeValues(t *testing.T) {
	if !validImportJobNo("0123456789abcdef0123456789abcdef") || validImportJobNo("../0123456789abcdef0123456789abc") || validImportJobNo("ABCDEF0123456789ABCDEF0123456789") {
		t.Fatal("job number validation is not fail-closed")
	}
}

func TestImportPreviewTTLAndLeaseAreExplicit(t *testing.T) {
	if importPreviewTTL != 30*time.Minute || importCommitLease < importPreviewTTL {
		t.Fatalf("ttl=%s lease=%s", importPreviewTTL, importCommitLease)
	}
}

func TestExpiredTakeoverInvalidatesOldImportWorkerToken(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	claimed := &ImportJob{ID: 8, TenantID: "tenant-a", ActorID: "user-a", Version: 3}
	locked := &ImportJob{ID: 8, TenantID: "tenant-a", ActorID: "user-a", Status: "COMMITTING", Version: 4, LockedBy: "new-token"}
	lockedUntil := now.Add(importCommitLease)
	locked.LockedUntil = &lockedUntil
	if validImportLease(locked, claimed, "old-token", now) {
		t.Fatal("old worker remained valid after lease takeover")
	}
	claimed.Version, claimed.LockedBy = 4, "new-token"
	if !validImportLease(locked, claimed, "new-token", now) {
		t.Fatal("current worker lease rejected")
	}
}

func TestImportClaimAllowsExpiredSameRequestTakeoverWithNewKey(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Second)
	job := &ImportJob{Status: "COMMITTING", CommitRequestVersion: 7, CommitIdempotencyKey: "old-key", LockedUntil: &expired}
	if err := validateImportClaim(job, 7, now); err != nil {
		t.Fatalf("same request version could not recover an expired lease with a new key: %v", err)
	}
	if err := validateImportClaim(job, 8, now); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different request version takeover error = %v", err)
	}
	active := now.Add(time.Second)
	job.LockedUntil = &active
	if err := validateImportClaim(job, 7, now); !errors.Is(err, ErrImportJobConflict) {
		t.Fatalf("active lease takeover error = %v", err)
	}
}

func TestCompletedImportReplayRequiresOriginalPreviewVersion(t *testing.T) {
	job := &ImportJob{Status: "COMPLETED", CommitRequestVersion: 7}
	if err := validateCompletedImportReplay(job, 7); err != nil {
		t.Fatalf("original preview version replay failed: %v", err)
	}
	if err := validateCompletedImportReplay(job, 8); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different version replay error = %v", err)
	}
	if err := validateCompletedImportReplay(&ImportJob{Status: "COMPLETED"}, 0); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("unbound completed replay error = %v", err)
	}
}

func TestFinishedAndNonImportableRowsDiscardEncryptedCommands(t *testing.T) {
	invalid := parsedImportRow{command: importCommand{ContactPhone: "13800138000"}, issues: []ImportRowIssue{{Code: "REQUIRED"}}}
	if status := importRowStatus(invalid.issues); status != "ERROR" {
		t.Fatalf("invalid row status = %s", status)
	}
	row := ImportRow{Status: "READY", CommandCipher: []byte("ciphertext")}
	row.Status, row.CommandCipher = "IMPORTED", nil
	if row.CommandCipher != nil {
		t.Fatal("finished row retained its encrypted import command")
	}
}

func TestCompletedImportResponseCountsPartialSuccessAndWarnings(t *testing.T) {
	completedAt := time.Date(2026, 8, 1, 8, 30, 0, 0, time.UTC)
	result := buildImportCommitResponse(&ImportJob{JobNo: "job", TotalRows: 3, Version: 2}, []ImportRow{
		{RowNo: 2, Status: "IMPORTED", CustomerNo: "KH202608010001"},
		{RowNo: 3, Status: "FAILED", ErrorCode: "DUPLICATE_CODE", ErrorMessage: "统一社会信用代码已存在"},
		{RowNo: 4, Status: "WARNING", ErrorCode: "POSSIBLE_DUPLICATE_NAME"},
	}, completedAt)
	if result.SucceededRows != 1 || result.FailedRows != 1 || result.SkippedRows != 1 || result.Version != 3 || result.Status != "COMPLETED" {
		t.Fatalf("unexpected partial result: %#v", result)
	}
}

func TestImportIdempotencyResponseRoundTrip(t *testing.T) {
	response := buildImportCommitResponse(&ImportJob{JobNo: "job", TotalRows: 1, Version: 2}, []ImportRow{{RowNo: 2, Status: "IMPORTED"}}, time.Now().UTC())
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var replay ImportCommitResponse
	if err = json.Unmarshal(encoded, &replay); err != nil || replay.JobNo != response.JobNo || replay.Version != response.Version || replay.SucceededRows != 1 {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
}

func TestPreviewImportBodyLimitConstants(t *testing.T) {
	if importMaxFileBytes != 10<<20 || importMaxDataRows != 1000 || importMultipartBodyLimit <= importMaxFileBytes {
		t.Fatal("unsafe import limits")
	}
}
