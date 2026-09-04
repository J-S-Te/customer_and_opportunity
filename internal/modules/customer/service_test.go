package customer

import (
	"context"
	"encoding/csv"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type exportRepositoryStub struct {
	Repository
	page pagination.Page[Response]
}

func (stub exportRepositoryStub) List(context.Context, auth.Principal, ListQuery) (pagination.Page[Response], error) {
	return stub.page, nil
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
	repository := exportRepositoryStub{page: pagination.Page[Response]{
		Items: []Response{{
			ID: 1, CustomerNo: "KH202609040001", Name: "示例客户", CustomerType: "企业", Industry: "软件",
			Region: "华东", OwnerUserID: "owner-1", OwnerOrgID: "org-1", Status: StatusVoid, CreatedAt: createdAt,
		}},
		Page: 1, PageSize: 100, Total: 1,
	}}
	directory := &ownerCatalogProbe{users: map[string]ownerdirectory.User{
		"owner-1": {ID: "owner-1", DisplayName: "张三", Organizations: []ownerdirectory.Organization{{ID: "org-1", Name: "华东销售部"}}},
	}}
	service := (&Service{repo: repository, now: func() time.Time { return createdAt }}).UseOwnerDirectory(directory)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "exporter"})
	file, err := service.CreateExport(ctx)
	if err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil && err != io.EOF {
		t.Fatalf("read export = %v", err)
	}
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
