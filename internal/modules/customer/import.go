package customer

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/audit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/safexlsx"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

const (
	importMaxFileBytes = 10 << 20
	importMaxDataRows  = 1000
	importPreviewTTL   = 30 * time.Minute
	importCommitLease  = 30 * time.Minute
)

var importHeaders = []string{
	"客户名称", "统一社会信用代码", "客户类型", "行业", "区域", "负责人用户ID",
	"负责人组织ID", "登记联系人姓名", "登记联系人电话", "登记联系人邮箱",
}

// ImportFileScanner is the mandatory trust boundary before workbook parsing.
// Implementations receive bytes only and must not persist them through this API.
type ImportFileScanner interface {
	Scan(context.Context, []byte) error
}

// ErrImportFileUnsafe is returned by scanner adapters only when their scan
// positively classifies the provided workbook bytes as unsafe. Transport,
// timeout and scanner-internal failures remain dependency-unavailable errors.
var ErrImportFileUnsafe = errors.New("customer import file is unsafe")

type importCommand struct {
	Name              string `json:"name"`
	UnifiedCreditCode string `json:"unified_credit_code"`
	CustomerType      string `json:"customer_type"`
	Industry          string `json:"industry"`
	Region            string `json:"region"`
	OwnerUserID       string `json:"owner_user_id"`
	OwnerOrgID        string `json:"owner_org_id"`
	ContactName       string `json:"contact_name"`
	ContactPhone      string `json:"contact_phone"`
	ContactEmail      string `json:"contact_email"`
}

type parsedImportRow struct {
	command importCommand
	preview ImportPreviewRowResponse
	issues  []ImportRowIssue
}

func (s *Service) PreviewImport(ctx context.Context, file []byte, reason string) (*ImportPreviewResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.imports == nil {
		return nil, ErrImportJobConflict
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 500 || unsafeText(reason) || len(file) == 0 || len(file) > importMaxFileBytes {
		return nil, ErrImportInvalidFile
	}
	if s.scanner == nil {
		return nil, ErrImportScannerUnavailable
	}
	if err = s.scanner.Scan(ctx, file); err != nil {
		if errors.Is(err, ErrImportFileUnsafe) {
			return nil, ErrImportScanFailed
		}
		return nil, ErrImportScannerUnavailable
	}
	workbook, err := safexlsx.ParseWorkbook(file, safexlsx.Limits{MaxArchiveBytes: importMaxFileBytes, MaxRows: importMaxDataRows + 1, MaxColumns: len(importHeaders), MaxCellRunes: 500})
	if err != nil {
		return nil, ErrImportInvalidFile
	}
	if err = validateImportHeader(workbook); err != nil {
		return nil, err
	}
	workbook = trimTrailingEmptyImportRows(workbook)
	if len(workbook) < 2 || len(workbook)-1 > importMaxDataRows {
		return nil, ErrImportInvalidFile
	}
	parsed := make([]parsedImportRow, 0, len(workbook)-1)
	nameCounts, codeCounts := make(map[string]int), make(map[string]int)
	for index, cells := range workbook[1:] {
		row := parseImportRow(uint32(index+2), cells)
		parsed = append(parsed, row)
		if row.command.Name != "" {
			nameCounts[normalizeName(row.command.Name)]++
		}
		if row.command.UnifiedCreditCode != "" {
			codeCounts[s.codec.HMAC(row.command.UnifiedCreditCode)]++
		}
	}
	for index := range parsed {
		row := &parsed[index]
		if row.command.UnifiedCreditCode != "" && codeCounts[s.codec.HMAC(row.command.UnifiedCreditCode)] > 1 {
			row.issues = append(row.issues, ImportRowIssue{Column: "统一社会信用代码", Code: "DUPLICATE_CODE_IN_FILE", Message: "统一社会信用代码在上传文件内重复"})
		}
		if row.command.Name != "" && nameCounts[normalizeName(row.command.Name)] > 1 {
			row.issues = append(row.issues, ImportRowIssue{Column: "客户名称", Code: "DUPLICATE_NAME_IN_FILE", Message: "客户名称在上传文件内疑似重复"})
		}
		if !hasImportError(row.issues) {
			duplicates, duplicateErr := s.repo.FindDuplicates(ctx, principal.TenantID, normalizeName(row.command.Name), s.codec.HMAC(row.command.UnifiedCreditCode), 0)
			if duplicateErr != nil {
				return nil, duplicateErr
			}
			exact := false
			for _, duplicate := range duplicates {
				exact = exact || duplicate.ExactCode
			}
			if exact {
				row.issues = append(row.issues, ImportRowIssue{Column: "统一社会信用代码", Code: "DUPLICATE_CODE", Message: "统一社会信用代码已存在"})
			} else if len(duplicates) > 0 {
				row.issues = append(row.issues, ImportRowIssue{Column: "客户名称", Code: "POSSIBLE_DUPLICATE_NAME", Message: "客户名称疑似已存在，当前批次不可导入"})
			}
		}
	}

	now := s.now()
	jobNo := requestctx.NewID()
	if !validImportJobNo(jobNo) {
		return nil, errors.New("customer import job id generation failed")
	}
	job := &ImportJob{TenantID: principal.TenantID, JobNo: jobNo, ActorID: principal.UserID, Status: "PREVIEWED", Reason: reason, TotalRows: uint32(len(parsed)), ExpiresAt: now.Add(importPreviewTTL), CreatedAt: now, UpdatedAt: now, Version: 1}
	rows := make([]ImportRow, 0, len(parsed))
	previewRows := make([]ImportPreviewRowResponse, 0, len(parsed))
	for _, parsedRow := range parsed {
		status := importRowStatus(parsedRow.issues)
		switch status {
		case "READY":
			job.ImportableRows++
		case "WARNING":
			job.WarningRows++
		default:
			job.ErrorRows++
		}
		var ciphertext []byte
		if status == "READY" {
			commandJSON, marshalErr := json.Marshal(parsedRow.command)
			if marshalErr != nil {
				return nil, marshalErr
			}
			var encryptErr error
			ciphertext, encryptErr = s.codec.Encrypt(string(commandJSON))
			if encryptErr != nil {
				return nil, encryptErr
			}
		}
		column, code, message := firstImportIssue(parsedRow.issues)
		rows = append(rows, ImportRow{TenantID: principal.TenantID, RowNo: parsedRow.preview.RowNo, Status: status, CommandCipher: ciphertext, ErrorColumn: column, ErrorCode: code, ErrorMessage: message, CreatedAt: now, UpdatedAt: now})
		parsedRow.preview.Status, parsedRow.preview.Issues = status, parsedRow.issues
		previewRows = append(previewRows, parsedRow.preview)
	}
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if createErr := s.imports.CreateImportPreview(txCtx, job, rows); createErr != nil {
			return createErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "IMPORT_PREVIEW", ResourceType: "customer_import", ResourceID: job.JobNo, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(importAuditSummary(job)), Reason: reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return &ImportPreviewResponse{JobNo: job.JobNo, Status: job.Status, Version: job.Version, TotalRows: job.TotalRows, ImportableRows: job.ImportableRows, WarningRows: job.WarningRows, ErrorRows: job.ErrorRows, ExpiresAt: job.ExpiresAt, Rows: previewRows}, nil
}

func validateImportHeader(rows [][]safexlsx.Cell) error {
	if len(rows) < 1 || len(rows)-1 > importMaxDataRows {
		return ErrImportInvalidFile
	}
	if len(rows[0]) != len(importHeaders) {
		return apperror.WithDetails(ErrImportInvalidFile, ImportRowIssue{Column: "表头", Code: "INVALID_HEADERS", Message: "表头数量不正确"})
	}
	seen := make(map[string]struct{}, len(importHeaders))
	for index, expected := range importHeaders {
		cell := rows[0][index]
		value := strings.TrimSpace(cell.Value)
		if cell.Formula || csvInjection(value) || value != expected {
			return apperror.WithDetails(ErrImportInvalidFile, ImportRowIssue{Column: expected, Code: "INVALID_HEADER", Message: "只接受固定中文表头且表头不得包含公式"})
		}
		if _, duplicate := seen[value]; duplicate {
			return apperror.WithDetails(ErrImportInvalidFile, ImportRowIssue{Column: expected, Code: "DUPLICATE_HEADER", Message: "表头重复"})
		}
		seen[value] = struct{}{}
	}
	return nil
}

func trimTrailingEmptyImportRows(rows [][]safexlsx.Cell) [][]safexlsx.Cell {
	end := len(rows)
	for end > 1 {
		empty := true
		for _, cell := range rows[end-1] {
			if cell.Formula || strings.TrimSpace(cell.Value) != "" {
				empty = false
				break
			}
		}
		if !empty {
			break
		}
		end--
	}
	return rows[:end]
}

func parseImportRow(rowNo uint32, cells []safexlsx.Cell) parsedImportRow {
	values := make([]string, len(importHeaders))
	issues := make([]ImportRowIssue, 0)
	if len(cells) != len(importHeaders) {
		issues = append(issues, ImportRowIssue{Column: "行", Code: "INVALID_COLUMN_COUNT", Message: "列数量不正确"})
	}
	for index := range values {
		if index >= len(cells) {
			continue
		}
		values[index] = strings.TrimSpace(cells[index].Value)
		if cells[index].Formula {
			issues = append(issues, ImportRowIssue{Column: importHeaders[index], Code: "FORMULA_NOT_ALLOWED", Message: "不允许公式单元格"})
		}
		if csvInjection(values[index]) {
			issues = append(issues, ImportRowIssue{Column: importHeaders[index], Code: "CSV_INJECTION_PREFIX", Message: "不允许公式或 CSV 注入前缀"})
		}
	}
	command := importCommand{Name: values[0], UnifiedCreditCode: values[1], CustomerType: values[2], Industry: values[3], Region: values[4], OwnerUserID: values[5], OwnerOrgID: values[6], ContactName: values[7], ContactPhone: values[8], ContactEmail: values[9]}
	required := []struct {
		index int
		max   int
	}{{0, 200}, {2, 64}, {3, 64}, {4, 64}, {5, 64}, {7, 100}, {8, 32}}
	for _, field := range required {
		if values[field.index] == "" {
			issues = append(issues, ImportRowIssue{Column: importHeaders[field.index], Code: "REQUIRED", Message: "必填字段为空"})
		} else if utf8.RuneCountInString(values[field.index]) > field.max || unsafeText(values[field.index]) {
			issues = append(issues, ImportRowIssue{Column: importHeaders[field.index], Code: "INVALID_VALUE", Message: "字段格式或长度不正确"})
		}
	}
	for _, field := range []struct {
		index int
		max   int
	}{{1, 64}, {6, 64}, {9, 200}} {
		if utf8.RuneCountInString(values[field.index]) > field.max || unsafeText(values[field.index]) {
			issues = append(issues, ImportRowIssue{Column: importHeaders[field.index], Code: "INVALID_VALUE", Message: "字段格式或长度不正确"})
		}
	}
	if command.ContactPhone != "" && !validPhone(command.ContactPhone) {
		issues = append(issues, ImportRowIssue{Column: "登记联系人电话", Code: "INVALID_PHONE", Message: "联系电话格式不正确"})
	}
	if command.ContactEmail != "" && !validEmail(command.ContactEmail) {
		issues = append(issues, ImportRowIssue{Column: "登记联系人邮箱", Code: "INVALID_EMAIL", Message: "联系邮箱格式不正确"})
	}
	preview := ImportPreviewRowResponse{RowNo: rowNo, Name: command.Name, UnifiedCreditCodeMasked: maskCreditCode(command.UnifiedCreditCode), CustomerType: command.CustomerType, Industry: command.Industry, Region: command.Region, OwnerUserID: command.OwnerUserID, ContactName: command.ContactName, ContactPhoneMasked: security.MaskPhone(command.ContactPhone), ContactEmailMasked: maskEmail(command.ContactEmail)}
	return parsedImportRow{command: command, preview: preview, issues: issues}
}

func importRowStatus(issues []ImportRowIssue) string {
	if hasImportError(issues) {
		return "ERROR"
	}
	if len(issues) > 0 {
		return "WARNING"
	}
	return "READY"
}

func hasImportError(issues []ImportRowIssue) bool {
	for _, issue := range issues {
		if issue.Code != "DUPLICATE_NAME_IN_FILE" && issue.Code != "POSSIBLE_DUPLICATE_NAME" {
			return true
		}
	}
	return false
}

func firstImportIssue(issues []ImportRowIssue) (string, string, string) {
	if len(issues) == 0 {
		return "", "", ""
	}
	return issues[0].Column, issues[0].Code, issues[0].Message
}

func csvInjection(value string) bool {
	value = strings.TrimLeft(value, " ")
	return value != "" && strings.ContainsRune("=+-@\t\r", []rune(value)[0])
}

func maskCreditCode(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= 8 {
		if len(runes) == 0 {
			return ""
		}
		return "****"
	}
	return string(runes[:4]) + "**********" + string(runes[len(runes)-4:])
}

func (s *Service) CommitImport(ctx context.Context, jobNo, idempotencyKey string, request ImportCommitRequest) (*ImportCommitResponse, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.imports == nil {
		return nil, ErrImportJobConflict
	}
	if !validImportJobNo(jobNo) {
		return nil, ErrImportJobNotFound
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, ErrIdempotencyRequired
	}
	requestHash := importRequestHash(jobNo, request.Version)
	var replay *ImportCommitResponse
	var job *ImportJob
	var rows []ImportRow
	lockToken := requestctx.NewID()
	if !validImportJobNo(lockToken) {
		return nil, errors.New("customer import lock token generation failed")
	}
	now := s.now()
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		existing, findErr := s.imports.FindImportIdempotency(txCtx, principal.TenantID, principal.UserID, idempotencyKey)
		if findErr != nil {
			return findErr
		}
		if existing != nil {
			if existing.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			if existing.Status == "COMPLETED" {
				if unmarshalErr := json.Unmarshal(existing.ResponseJSON, &replay); unmarshalErr != nil {
					return unmarshalErr
				}
				return nil
			}
			if existing.Status != "PROCESSING" {
				return ErrImportJobConflict
			}
			completed, completedRows, completedErr := s.imports.FindImportPreview(txCtx, principal.TenantID, principal.UserID, jobNo)
			if completedErr != nil {
				return completedErr
			}
			if completed.Status == "COMPLETED" {
				if replayErr := validateCompletedImportReplay(completed, request.Version); replayErr != nil {
					return replayErr
				}
				completedAt := completed.UpdatedAt
				if completed.CompletedAt != nil {
					completedAt = *completed.CompletedAt
				}
				replay = buildImportCommitResponse(completed, completedRows, completedAt)
				replay.Version = completed.Version
				encoded, encodeErr := json.Marshal(replay)
				if encodeErr != nil {
					return encodeErr
				}
				existing.ResponseJSON, existing.Status = encoded, "COMPLETED"
				return s.imports.CompleteImportIdempotency(txCtx, existing)
			}
		} else {
			completed, completedRows, completedErr := s.imports.FindImportPreview(txCtx, principal.TenantID, principal.UserID, jobNo)
			if completedErr != nil {
				return completedErr
			}
			if completed.Status == "COMPLETED" {
				if replayErr := validateCompletedImportReplay(completed, request.Version); replayErr != nil {
					return replayErr
				}
				completedAt := completed.UpdatedAt
				if completed.CompletedAt != nil {
					completedAt = *completed.CompletedAt
				}
				replay = buildImportCommitResponse(completed, completedRows, completedAt)
				replay.Version = completed.Version
				encoded, encodeErr := json.Marshal(replay)
				if encodeErr != nil {
					return encodeErr
				}
				idempotency := &ImportIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: idempotencyKey, RequestHash: requestHash, Status: "COMPLETED", ResponseJSON: encoded, CreatedAt: now}
				if createErr := s.imports.CreateImportIdempotency(txCtx, idempotency); createErr != nil {
					return mapImportIdempotencyError(createErr)
				}
				return nil
			}
			idempotency := &ImportIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: idempotencyKey, RequestHash: requestHash, Status: "PROCESSING", CreatedAt: now}
			if createErr := s.imports.CreateImportIdempotency(txCtx, idempotency); createErr != nil {
				return mapImportIdempotencyError(createErr)
			}
		}
		var claimErr error
		job, rows, claimErr = s.imports.ClaimImport(txCtx, principal.TenantID, principal.UserID, jobNo, request.Version, idempotencyKey, lockToken, now, now.Add(importCommitLease))
		return claimErr
	})
	if err != nil || replay != nil {
		return replay, err
	}

	for index := range rows {
		row := &rows[index]
		if row.Status != "READY" {
			continue
		}
		err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
			leaseNow := s.now()
			if renewErr := s.imports.LockAndRenewImportLease(txCtx, job, lockToken, leaseNow, leaseNow.Add(importCommitLease)); renewErr != nil {
				return renewErr
			}
			return s.commitImportRow(txCtx, principal, job, row)
		})
		if err != nil {
			return nil, err
		}
	}
	completedAt := s.now()
	result := buildImportCommitResponse(job, rows, completedAt)
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	job.Status, job.CompletedAt, job.UpdatedAt = "COMPLETED", &completedAt, completedAt
	job.SucceededRows, job.FailedRows, job.Version = result.SucceededRows, result.FailedRows, job.Version+1
	idempotency := &ImportIdempotency{TenantID: principal.TenantID, ActorID: principal.UserID, Key: idempotencyKey, RequestHash: requestHash, Status: "COMPLETED", ResponseJSON: encoded}
	err = database.WithTransaction(ctx, s.db, func(txCtx context.Context) error {
		if completeErr := s.imports.CompleteImport(txCtx, job, lockToken, completedAt, idempotency); completeErr != nil {
			return completeErr
		}
		return s.audit.Write(txCtx, audit.Event{TenantID: principal.TenantID, Module: "customer", Operation: "IMPORT_COMMIT", ResourceType: "customer_import", ResourceID: job.JobNo, ActorID: principal.UserID, ActorNameSnapshot: principal.DisplayName, AfterJSON: audit.JSON(importAuditSummary(job)), Reason: job.Reason, Result: "SUCCESS"})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// validateCompletedImportReplay ensures a new idempotency key cannot be used
// to reinterpret an already-completed import with a different preview version.
// Replays with the original key are independently bound by request_hash.
func validateCompletedImportReplay(job *ImportJob, requestVersion uint64) error {
	if job == nil || job.Status != "COMPLETED" {
		return ErrImportJobConflict
	}
	if job.CommitRequestVersion == 0 || job.CommitRequestVersion != requestVersion {
		return ErrIdempotencyConflict
	}
	return nil
}

func (s *Service) commitImportRow(ctx context.Context, principal auth.Principal, job *ImportJob, row *ImportRow) error {
	plaintext, err := s.codec.Decrypt(row.CommandCipher)
	if err != nil {
		return err
	}
	var command importCommand
	if err = json.Unmarshal([]byte(plaintext), &command); err != nil {
		return err
	}
	duplicates, err := s.repo.FindDuplicates(ctx, principal.TenantID, normalizeName(command.Name), s.codec.HMAC(command.UnifiedCreditCode), 0)
	if err != nil {
		return err
	}
	code, message := "", ""
	for _, duplicate := range duplicates {
		if duplicate.ExactCode {
			code, message = "DUPLICATE_CODE", "统一社会信用代码已存在"
			break
		}
	}
	if code == "" && len(duplicates) > 0 {
		code, message = "POSSIBLE_DUPLICATE_NAME", "客户名称疑似已存在"
	}
	row.UpdatedAt = s.now()
	if code != "" {
		row.Status, row.ErrorCode, row.ErrorMessage, row.CommandCipher = "FAILED", code, message, nil
		return s.imports.UpdateImportRow(ctx, row)
	}
	creditCipher, err := s.codec.Encrypt(command.UnifiedCreditCode)
	if err != nil {
		return err
	}
	phoneCipher, err := s.codec.Encrypt(command.ContactPhone)
	if err != nil {
		return err
	}
	emailCipher, err := s.codec.Encrypt(command.ContactEmail)
	if err != nil {
		return err
	}
	var creditHMAC *string
	if command.UnifiedCreditCode != "" {
		value := s.codec.HMAC(command.UnifiedCreditCode)
		creditHMAC = &value
	}
	model := &Customer{Name: command.Name, NormalizedName: normalizeName(command.Name), UnifiedCreditCodeCipher: creditCipher, UnifiedCreditCodeHMAC: creditHMAC, CustomerType: command.CustomerType, Industry: command.Industry, Region: command.Region, OwnerUserID: command.OwnerUserID, OwnerOrgID: command.OwnerOrgID, Status: StatusActive}
	model.TenantID, model.CreatedBy, model.UpdatedBy, model.Version = principal.TenantID, principal.UserID, principal.UserID, 1
	model.Contacts = []Contact{{Model: database.Model{TenantID: principal.TenantID, CreatedBy: principal.UserID, UpdatedBy: principal.UserID, Version: 1}, Name: command.ContactName, PhoneCipher: phoneCipher, PhoneMasked: security.MaskPhone(command.ContactPhone), EmailCipher: emailCipher, EmailMasked: maskEmail(command.ContactEmail), IsRegistration: true}}
	if err = s.persistCreatedCustomer(ctx, principal, model, job.Reason); err != nil {
		if errors.Is(err, ErrDuplicateCode) || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			row.Status, row.ErrorCode, row.ErrorMessage = "FAILED", "DUPLICATE", "客户与现有数据冲突"
			return s.imports.UpdateImportRow(ctx, row)
		}
		return err
	}
	row.Status, row.CustomerID, row.CustomerNo, row.CommandCipher = "IMPORTED", &model.ID, model.CustomerNo, nil
	return s.imports.UpdateImportRow(ctx, row)
}

func buildImportCommitResponse(job *ImportJob, rows []ImportRow, completedAt time.Time) *ImportCommitResponse {
	result := &ImportCommitResponse{JobNo: job.JobNo, Status: "COMPLETED", Version: job.Version + 1, TotalRows: job.TotalRows, CompletedAt: completedAt, Rows: make([]ImportCommitRowResponse, 0, len(rows))}
	for _, row := range rows {
		item := ImportCommitRowResponse{RowNo: row.RowNo, Status: row.Status, CustomerNo: row.CustomerNo, ErrorCode: row.ErrorCode, Message: row.ErrorMessage}
		if row.CustomerID != nil {
			item.CustomerID = *row.CustomerID
		}
		switch row.Status {
		case "IMPORTED":
			result.SucceededRows++
		case "FAILED":
			result.FailedRows++
		default:
			result.SkippedRows++
		}
		result.Rows = append(result.Rows, item)
	}
	return result
}

func (s *Service) ImportErrorsCSV(ctx context.Context, jobNo string) ([]byte, string, error) {
	principal, err := principalFromContext(ctx)
	if err != nil {
		return nil, "", err
	}
	if s.imports == nil {
		return nil, "", ErrImportJobConflict
	}
	if !validImportJobNo(jobNo) {
		return nil, "", ErrImportJobNotFound
	}
	job, rows, err := s.imports.FindImportPreview(ctx, principal.TenantID, principal.UserID, jobNo)
	if err != nil {
		return nil, "", err
	}
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	_ = writer.Write([]string{"行号", "状态", "错误列", "错误代码", "错误信息", "客户编号"})
	for _, row := range rows {
		if row.Status == "READY" || row.Status == "IMPORTED" {
			continue
		}
		_ = writer.Write([]string{fmt.Sprint(row.RowNo), safeCSV(row.Status), safeCSV(row.ErrorColumn), safeCSV(row.ErrorCode), safeCSV(row.ErrorMessage), safeCSV(row.CustomerNo)})
	}
	writer.Flush()
	if err = writer.Error(); err != nil {
		return nil, "", err
	}
	return []byte(builder.String()), "customer-import-" + safeFilename(job.JobNo) + "-errors.csv", nil
}

func importRequestHash(jobNo string, version uint64) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(jobNo) + "\x00" + fmt.Sprint(version)))
	return hex.EncodeToString(sum[:])
}

func mapImportIdempotencyError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return ErrImportJobConflict
	}
	return err
}

func importAuditSummary(job *ImportJob) map[string]any {
	return map[string]any{"job_no": job.JobNo, "status": job.Status, "total_rows": job.TotalRows, "importable_rows": job.ImportableRows, "warning_rows": job.WarningRows, "error_rows": job.ErrorRows, "succeeded_rows": job.SucceededRows, "failed_rows": job.FailedRows}
}

func safeCSV(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if csvInjection(value) {
		return "'" + value
	}
	return value
}

func safeFilename(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			builder.WriteRune(char)
		}
	}
	if builder.Len() == 0 {
		return "errors"
	}
	return builder.String()
}

func validImportJobNo(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
