package customer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

// exportRepositoryStub 记录导出实际下发的 ListQuery；pages 非空时按页码返回多页结果，
// 用于验证导出翻页而不是依赖固定的第一页。
type exportRepositoryStub struct {
	Repository
	page    pagination.Page[Response]
	pages   []pagination.Page[Response]
	queries []ListQuery
}

func (stub *exportRepositoryStub) List(_ context.Context, _ auth.Principal, query ListQuery) (pagination.Page[Response], error) {
	stub.queries = append(stub.queries, query)
	if len(stub.pages) > 0 {
		if query.Page >= 1 && query.Page <= len(stub.pages) {
			return stub.pages[query.Page-1], nil
		}
		return pagination.Page[Response]{Page: query.Page, PageSize: query.PageSize, Items: []Response{}}, nil
	}
	return stub.page, nil
}

// readCSVExport 读取导出文件并校验 UTF-8 BOM：Excel 中文环境依赖 BOM 识别 UTF-8。
func readCSVExport(t *testing.T, file *os.File) [][]string {
	t.Helper()
	raw, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read export = %v", err)
	}
	if !strings.HasPrefix(string(raw), "\xef\xbb\xbf") {
		t.Fatalf("export missing UTF-8 BOM: %q", raw[:min(8, len(raw))])
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), "\xef\xbb\xbf"))).ReadAll()
	if err != nil && err != io.EOF {
		t.Fatalf("parse export = %v", err)
	}
	return records
}

func TestNormalizeName(t *testing.T) {
	if got := normalizeName("  上海 示例 科技  "); got != "上海示例科技" {
		t.Fatalf("got %q", got)
	}
}

func TestCustomerMasterDataRejectsWhitespace(t *testing.T) {
	if err := validateCustomerMasterData("客户", "企业", "制造业", "华东", "维护资料"); err != nil {
		t.Fatalf("valid customer data rejected: %v", err)
	}
	if err := validateCustomerMasterData(" ", "企业", "制造业", "华东", "维护资料"); err != ErrInvalidMasterData {
		t.Fatalf("blank customer name returned %v", err)
	}
	if err := validateCustomerMasterData("客户", "企业", "制造业", "华东", "   "); err != ErrInvalidMasterData {
		t.Fatalf("blank reason returned %v", err)
	}
}

func TestCustomerMasterDataRejectsNumericPlaceholders(t *testing.T) {
	if err := validateCustomerMasterData("1", "企业", "制造业", "华东", "维护资料"); err != ErrInvalidMasterData {
		t.Fatalf("numeric customer name returned %v", err)
	}
	if err := validateCustomerMasterData("3M公司", "业主", "制造业", "华东", "维护资料"); err != nil {
		t.Fatalf("meaningful mixed customer name rejected: %v", err)
	}
}
func TestLeftPad4RejectsExhaustedSequence(t *testing.T) {
	if got := leftPad4(42); got != "0042" {
		t.Fatalf("got %q", got)
	}
	if got := leftPad4(10000); got != "" {
		t.Fatalf("overflow wrapped to %q", got)
	}
}
func TestMaskEmail(t *testing.T) {
	if got := maskEmail("alice@example.com"); got != "a***@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := maskEmail(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := maskEmail("@example.com"); got != "" {
		t.Fatalf("invalid local part was masked as %q", got)
	}
	if got := maskEmail("alice@"); got != "" {
		t.Fatalf("invalid domain was masked as %q", got)
	}
}

func TestCustomerFollowupResponseDoesNotExposeTenantFields(t *testing.T) {
	model := &Followup{Type: "PHONE", Content: "hello", FollowedBy: "user", FollowedAt: time.Now()}
	response := toFollowupResponse(model)
	if response.Type != "PHONE" || response.Content != "hello" || response.FollowedBy != "user" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestValidateRegistrationContacts(t *testing.T) {
	if err := validateRegistrationContacts([]ContactInput{{Name: "登记人", Phone: "13800000000", IsRegistration: true}}); err != nil {
		t.Fatalf("valid contacts rejected: %v", err)
	}
	if err := validateRegistrationContacts([]ContactInput{{Name: "无登记人"}}); err != ErrInvalidContact {
		t.Fatalf("missing registration contact: %v", err)
	}
	if err := validateRegistrationContacts([]ContactInput{{Name: " ", Phone: "13800000000", IsRegistration: true}}); err != ErrInvalidContact {
		t.Fatalf("blank contact name accepted: %v", err)
	}
}

func TestValidateUpdateRegistrationContacts(t *testing.T) {
	valid := []UpdateContactInput{{Name: "登记人", IsRegistration: true}, {Name: "其他人"}}
	if err := validateUpdateRegistrationContacts(valid); err != nil {
		t.Fatalf("valid contacts rejected: %v", err)
	}
	if err := validateUpdateRegistrationContacts([]UpdateContactInput{{Name: "无登记人"}}); err != ErrInvalidContact {
		t.Fatalf("missing registration contact: %v", err)
	}
	if err := validateUpdateRegistrationContacts([]UpdateContactInput{{IsRegistration: true}, {IsRegistration: true}}); err != ErrInvalidContact {
		t.Fatalf("multiple registration contacts: %v", err)
	}
}

func TestBuildChangeLogsAreFieldLevelAndMasked(t *testing.T) {
	before := Response{Name: "旧名称", Contacts: []ContactResponse{{ID: 1, PhoneMasked: "138****0000"}}}
	after := Response{Name: "新名称", Contacts: []ContactResponse{{ID: 1, PhoneMasked: "139****0000"}}}
	logs := buildChangeLogs(auth.Principal{TenantID: "tenant", UserID: "user"}, 9, before, after, "reason", "req", time.Now())
	if len(logs) != 2 || logs[0].FieldName != "name" || logs[1].FieldName != "contacts" {
		t.Fatalf("unexpected logs: %#v", logs)
	}
	for _, log := range logs {
		if string(log.AfterJSON) == "" || log.RequestID != "req" {
			t.Fatalf("incomplete log: %#v", log)
		}
	}
}

func TestCustomerResponseExposesMergeTraceWithoutSensitiveValues(t *testing.T) {
	targetID := uint64(22)
	endDate := time.Date(2026, 8, 1, 15, 30, 0, 0, time.UTC)
	model := &Customer{Model: database.Model{ID: 11}, Status: StatusMerged, MergedIntoID: &targetID, EndDate: &endDate}
	result := toResponse(model)
	if result.MergedIntoID == nil || *result.MergedIntoID != targetID || result.EndDate == nil || *result.EndDate != "2026-08-01" {
		t.Fatalf("merge trace missing: %#v", result)
	}
}

func TestCreateExportUsesOwnerAndOrganizationNamesAndChineseStatus(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	repository := &exportRepositoryStub{page: pagination.Page[Response]{
		Items: []Response{{
			ID: 1, CustomerNo: "KH202609040001", Name: "示例客户", CustomerType: "企业", Industry: "软件",
			Region: "华东", OwnerUserID: "owner-1", OwnerOrgID: "org-1", Status: StatusVoid, CreatedAt: createdAt,
		}},
		Page: 1, PageSize: 100, Total: 1,
	}}
	directory := &ownerCatalogProbe{users: map[string]ownerdirectory.User{
		"owner-1": {ID: "owner-1", DisplayName: "张三", Organizations: []ownerdirectory.Organization{{ID: "org-1", Name: "华东销售部"}}},
	}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).UseOwnerDirectory(directory).UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})
	file, err := service.CreateExport(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	records := readCSVExport(t, file)
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if got, want := records[0], []string{"客户编号", "客户名称", "客户类型", "行业", "区域", "负责人姓名", "负责人组织名称", "状态", "创建时间"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("headers = %#v, want %#v", got, want)
	}
	if got := records[1]; got[5] != "张三" || got[6] != "华东销售部" || got[7] != "已作废" {
		t.Fatalf("export labels = %#v", got)
	}
	if contents := strings.Join(records[0], "|") + "|" + strings.Join(records[1], "|"); strings.Contains(contents, "owner-1") || strings.Contains(contents, "org-1") {
		t.Fatalf("export leaked internal owner identifiers: %q", contents)
	}
}

func TestCreateExportEscapesCSVFormulaInjection(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	repository := &exportRepositoryStub{page: pagination.Page[Response]{
		Items: []Response{{
			// 名称携带 =HYPERLINK 载荷、编号带前导 tab，均来自 customer.create 可控输入。
			ID: 1, CustomerNo: "\tKH202609040001", Name: `=HYPERLINK("https://evil.example","x")`, CustomerType: "企业", Industry: "软件",
			Region: "华东", OwnerUserID: "owner-1", OwnerOrgID: "org-1", Status: StatusActive, CreatedAt: createdAt,
		}},
		Page: 1, PageSize: 100, Total: 1,
	}}
	directory := &ownerCatalogProbe{users: map[string]ownerdirectory.User{
		"owner-1": {ID: "owner-1", DisplayName: "张三", Organizations: []ownerdirectory.Organization{{ID: "org-1", Name: "华东销售部"}}},
	}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).UseOwnerDirectory(directory).UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})
	file, err := service.CreateExport(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	records := readCSVExport(t, file)
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	// 公式载荷与前导 tab 都必须带 ' 前缀，按字面文本渲染。
	if got, want := records[1][1], `'=HYPERLINK("https://evil.example","x")`; got != want {
		t.Fatalf("name cell = %q, want %q", got, want)
	}
	if got, want := records[1][0], "'\tKH202609040001"; got != want {
		t.Fatalf("customer no cell = %q, want %q", got, want)
	}
	// 普通中文与常规值不受转义影响。
	for index, want := range map[int]string{2: "企业", 3: "软件", 4: "华东", 5: "张三", 6: "华东销售部", 7: "正常"} {
		if got := records[1][index]; got != want {
			t.Fatalf("cell[%d] = %q, want %q", index, got, want)
		}
	}
	if got, want := records[1][8], "2026-09-04T02:03:04Z"; got != want {
		t.Fatalf("created at cell = %q, want %q", got, want)
	}
}

func TestCreateExportPassesFiltersAndUsesFixedPageSize(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	createdFrom := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	createdTo := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	lastFollowupFrom := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	lastFollowupTo := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	repository := &exportRepositoryStub{page: pagination.Page[Response]{
		Items: []Response{{ID: 1, CustomerNo: "KH202609040001", Name: "示例客户", Status: StatusActive, CreatedAt: createdAt}},
		Page:  1, PageSize: 100, Total: 1,
	}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).
		UseOwnerDirectory(&ownerCatalogProbe{users: map[string]ownerdirectory.User{}}).
		UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})
	file, err := service.CreateExport(ctx, ListQuery{
		Keyword: "示例", CustomerType: "企业", Industry: "软件", Region: "华东", OwnerID: "owner-1",
		Status: StatusActive, CreditLevel: "A", QuickFilter: QuickFilterNew,
		CreatedFrom: &createdFrom, CreatedTo: &createdTo,
		LastFollowupFrom: &lastFollowupFrom, LastFollowupTo: &lastFollowupTo,
		Page: 7, PageSize: 20, SortBy: "name", SortOrder: "asc",
	})
	if err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	if len(repository.queries) != 1 {
		t.Fatalf("List() calls = %d, want 1", len(repository.queries))
	}
	got := repository.queries[0]
	// 调用方的展示分页不生效：导出固定从第 1 页开始、每页 100 条。
	if got.Page != 1 || got.PageSize != createExportPageSize {
		t.Fatalf("pagination = %d/%d, want 1/%d", got.Page, got.PageSize, createExportPageSize)
	}
	if got.Keyword != "示例" || got.CustomerType != "企业" || got.Industry != "软件" || got.Region != "华东" || got.OwnerID != "owner-1" || got.Status != StatusActive || got.CreditLevel != "A" || got.QuickFilter != QuickFilterNew {
		t.Fatalf("filters not forwarded: %#v", got)
	}
	if got.CreatedFrom == nil || !got.CreatedFrom.Equal(createdFrom) || got.CreatedTo == nil || !got.CreatedTo.Equal(createdTo) {
		t.Fatalf("created range = %v~%v, want %v~%v", got.CreatedFrom, got.CreatedTo, createdFrom, createdTo)
	}
	if got.LastFollowupFrom == nil || !got.LastFollowupFrom.Equal(lastFollowupFrom) || got.LastFollowupTo == nil || !got.LastFollowupTo.Equal(lastFollowupTo) {
		t.Fatalf("last followup range = %v~%v, want %v~%v", got.LastFollowupFrom, got.LastFollowupTo, lastFollowupFrom, lastFollowupTo)
	}
	if got.SortBy != "name" || got.SortOrder != "asc" {
		t.Fatalf("sort = %q %q, want name asc", got.SortBy, got.SortOrder)
	}
}

func TestCreateExportPaginatesWithFixedSizeAndStableDefaultSort(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	buildPage := func(start, count int) pagination.Page[Response] {
		items := make([]Response, 0, count)
		for index := start; index < start+count; index++ {
			items = append(items, Response{ID: uint64(index), CustomerNo: fmt.Sprintf("KH%04d", index), Name: fmt.Sprintf("客户%04d", index), Status: StatusActive, CreatedAt: createdAt})
		}
		return pagination.Page[Response]{Items: items, Page: 1, PageSize: createExportPageSize, Total: 150}
	}
	repository := &exportRepositoryStub{pages: []pagination.Page[Response]{buildPage(1, 100), buildPage(101, 50)}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).
		UseOwnerDirectory(&ownerCatalogProbe{users: map[string]ownerdirectory.User{}}).
		UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})
	file, err := service.CreateExport(ctx, ListQuery{Keyword: "客户"})
	if err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	records := readCSVExport(t, file)
	if len(records) != 151 {
		t.Fatalf("record count = %d, want 151", len(records))
	}
	if records[1][0] != "KH0001" || records[150][0] != "KH0150" {
		t.Fatalf("rows out of order: first=%q last=%q", records[1][0], records[150][0])
	}
	// 两页都要保留筛选；未指定排序时固定 created_at asc，避免导出顺序漂移。
	if len(repository.queries) != 2 {
		t.Fatalf("List() calls = %d, want 2", len(repository.queries))
	}
	for index, query := range repository.queries {
		if query.Page != index+1 || query.PageSize != createExportPageSize {
			t.Fatalf("page %d query = %d/%d, want %d/%d", index, query.Page, query.PageSize, index+1, createExportPageSize)
		}
		if query.Keyword != "客户" {
			t.Fatalf("page %d keyword = %q, want 客户", index, query.Keyword)
		}
		if query.SortBy != "created_at" || query.SortOrder != "asc" {
			t.Fatalf("page %d sort = %q %q, want created_at asc", index, query.SortBy, query.SortOrder)
		}
	}
}

// exportTempFileNames 列出当前进程临时目录里的客户导出文件，用于断言失败路径没有留下临时文件。
func exportTempFileNames(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir = %v", err)
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "crm-customer-export-") {
			names[entry.Name()] = struct{}{}
		}
	}
	return names
}

func assertNoNewExportTempFile(t *testing.T, before map[string]struct{}) {
	t.Helper()
	for name := range exportTempFileNames(t) {
		if _, existed := before[name]; !existed {
			t.Fatalf("rejected export left temp file %q behind", name)
		}
	}
}

func TestExportRowLimitConstantAndMessage(t *testing.T) {
	if createExportMaxRows != 50000 {
		t.Fatalf("createExportMaxRows = %d, want 50000", createExportMaxRows)
	}
	if createExportRowLimit != createExportMaxRows {
		t.Fatalf("runtime export limit = %d, want default %d", createExportRowLimit, createExportMaxRows)
	}
	appErr := apperror.As(newExportRowLimitError(createExportMaxRows))
	if appErr.HTTPStatus != http.StatusUnprocessableEntity || appErr.Code != "CRM_CUSTOMER_EXPORT_ROW_LIMIT_EXCEEDED" {
		t.Fatalf("limit error = %d/%s, want 422/CRM_CUSTOMER_EXPORT_ROW_LIMIT_EXCEEDED", appErr.HTTPStatus, appErr.Code)
	}
	if !strings.Contains(appErr.Message, "导出数据量超过上限（50000 行），请缩小筛选范围后重试") {
		t.Fatalf("limit error message = %q", appErr.Message)
	}
}

// TestCreateExportRejectsTotalAboveRowLimit 用 Total 直接超限的仓储桩验证提前拒绝：
// 不必真的构造 5 万行，且失败后不能留下临时文件、也不能返回部分文件。
func TestCreateExportRejectsTotalAboveRowLimit(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	repository := &exportRepositoryStub{page: pagination.Page[Response]{
		Items: []Response{{ID: 1, CustomerNo: "KH0001", Name: "客户0001", Status: StatusActive, CreatedAt: createdAt}},
		Page:  1, PageSize: createExportPageSize, Total: createExportMaxRows + 1,
	}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).
		UseOwnerDirectory(&ownerCatalogProbe{users: map[string]ownerdirectory.User{}}).
		UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})

	before := exportTempFileNames(t)
	file, err := service.CreateExport(ctx, ListQuery{})
	if file != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		t.Fatalf("CreateExport() returned a partial file for an over-limit export")
	}
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("CreateExport() error = %v, want apperror", err)
	}
	if appErr.HTTPStatus != http.StatusUnprocessableEntity || appErr.Code != "CRM_CUSTOMER_EXPORT_ROW_LIMIT_EXCEEDED" {
		t.Fatalf("error = %d/%s, want 422/CRM_CUSTOMER_EXPORT_ROW_LIMIT_EXCEEDED", appErr.HTTPStatus, appErr.Code)
	}
	if !strings.Contains(appErr.Message, "请缩小筛选范围后重试") {
		t.Fatalf("error message = %q", appErr.Message)
	}
	assertNoNewExportTempFile(t, before)
}

// TestCreateExportStopsWhenAccumulatedRowsExceedLimit 注入较小的上限，验证分页过程中
// Total 未能提前暴露规模（数据在翻页间增长）时，仍以累计已写行数为准整体失败。
func TestCreateExportStopsWhenAccumulatedRowsExceedLimit(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	original := createExportRowLimit
	createExportRowLimit = 150
	t.Cleanup(func() { createExportRowLimit = original })

	// Total 仍是上限内的旧快照，但仓储实际返回了更多行：只能靠累计行数发现超限。
	items := make([]Response, 0, 200)
	for index := 1; index <= 200; index++ {
		items = append(items, Response{ID: uint64(index), CustomerNo: fmt.Sprintf("KH%04d", index), Name: fmt.Sprintf("客户%04d", index), Status: StatusActive, CreatedAt: createdAt})
	}
	repository := &exportRepositoryStub{page: pagination.Page[Response]{Items: items, Page: 1, PageSize: createExportPageSize, Total: 150}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).
		UseOwnerDirectory(&ownerCatalogProbe{users: map[string]ownerdirectory.User{}}).
		UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})

	before := exportTempFileNames(t)
	file, err := service.CreateExport(ctx, ListQuery{})
	if file != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		t.Fatalf("CreateExport() returned a partial file instead of failing at the accumulated limit")
	}
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || appErr.HTTPStatus != http.StatusUnprocessableEntity || appErr.Code != "CRM_CUSTOMER_EXPORT_ROW_LIMIT_EXCEEDED" {
		t.Fatalf("CreateExport() error = %v, want 422 CRM_CUSTOMER_EXPORT_ROW_LIMIT_EXCEEDED", err)
	}
	if !strings.Contains(appErr.Message, "（150 行）") {
		t.Fatalf("error message = %q, want injected limit", appErr.Message)
	}
	assertNoNewExportTempFile(t, before)
}

// TestCreateExportWithinLimitKeepsEveryPageAndFilters 验证上限内多页导出依然完整，
// 且每页都透传同一套筛选与稳定排序。
func TestCreateExportWithinLimitKeepsEveryPageAndFilters(t *testing.T) {
	createdAt := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	buildPage := func(start, count int) pagination.Page[Response] {
		items := make([]Response, 0, count)
		for index := start; index < start+count; index++ {
			items = append(items, Response{ID: uint64(index), CustomerNo: fmt.Sprintf("KH%04d", index), Name: fmt.Sprintf("客户%04d", index), Status: StatusActive, CreatedAt: createdAt})
		}
		return pagination.Page[Response]{Items: items, Page: 1, PageSize: createExportPageSize, Total: 250}
	}
	repository := &exportRepositoryStub{pages: []pagination.Page[Response]{buildPage(1, 100), buildPage(101, 100), buildPage(201, 50)}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).
		UseOwnerDirectory(&ownerCatalogProbe{users: map[string]ownerdirectory.User{}}).
		UseFreezeChecker(allowAllFreezeChecker{})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})
	file, err := service.CreateExport(ctx, ListQuery{Keyword: "客户", Region: "华东", Status: StatusActive, SortBy: "name", SortOrder: "asc"})
	if err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	records := readCSVExport(t, file)
	if len(records) != 251 {
		t.Fatalf("record count = %d, want 251", len(records))
	}
	if records[1][0] != "KH0001" || records[250][0] != "KH0250" {
		t.Fatalf("rows out of order or truncated: first=%q last=%q", records[1][0], records[250][0])
	}
	if len(repository.queries) != 3 {
		t.Fatalf("List() calls = %d, want 3", len(repository.queries))
	}
	for index, query := range repository.queries {
		if query.Page != index+1 || query.PageSize != createExportPageSize {
			t.Fatalf("page %d query = %d/%d, want %d/%d", index, query.Page, query.PageSize, index+1, createExportPageSize)
		}
		if query.Keyword != "客户" || query.Region != "华东" || query.Status != StatusActive {
			t.Fatalf("page %d filters = %#v", index, query)
		}
		if query.SortBy != "name" || query.SortOrder != "asc" {
			t.Fatalf("page %d sort = %q %q, want name asc", index, query.SortBy, query.SortOrder)
		}
	}
}

func TestCustomerStatusLabelUsesChineseLabels(t *testing.T) {
	for status, want := range map[string]string{
		StatusActive: "正常",
		StatusVoid:   "已作废",
		StatusMerged: "已合并",
		"future":     "未知",
	} {
		if got := customerStatusLabel(status); got != want {
			t.Fatalf("customerStatusLabel(%q) = %q, want %q", status, got, want)
		}
	}
}
