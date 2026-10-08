package customer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/safexlsx"
)

type importOwnerCatalogStub struct {
	users                []ownerdirectory.User
	listErr, validateErr error
	calls, validations   int
	total                int64
	tenant               string
}

func (s *importOwnerCatalogStub) List(ctx context.Context, q ownerdirectory.Query) (ownerdirectory.Page, error) {
	s.calls++
	p, _ := auth.FromContext(ctx)
	if s.tenant != "" && p.TenantID != s.tenant {
		return ownerdirectory.Page{}, ownerdirectory.ErrUnavailable
	}
	if q.UserID != "a" || q.PageSize != 2 {
		return ownerdirectory.Page{}, ownerdirectory.ErrUnavailable
	}
	return ownerdirectory.Page{Items: s.users, Total: s.total}, s.listErr
}
func (s *importOwnerCatalogStub) Validate(context.Context, string, string) error {
	s.validations++
	return s.validateErr
}
func (s *importOwnerCatalogStub) Resolve(context.Context, []string) (map[string]ownerdirectory.User, error) {
	panic("unexpected Resolve")
}
func importTestCatalog() *importOwnerCatalogStub {
	return &importOwnerCatalogStub{total: 1, users: []ownerdirectory.User{{ID: "a", DisplayName: "张三", Organizations: []ownerdirectory.Organization{{ID: "sales", Name: "销售部", IsPrimary: true}}}}}
}
func TestImportDefaultsToValidatedActorAndCachesDirectory(t *testing.T) {
	catalog := importTestCatalog()
	resolver := newImportOwnerResolver(catalog, "a")
	for i := 0; i < 1000; i++ {
		row := parsedImportRow{}
		resolver.resolveRow(context.Background(), &row, false)
		if len(row.issues) != 0 || row.command.OwnerUserID != "a" || row.command.OwnerOrgID != "sales" || row.preview.OwnerDisplayName != "张三" || row.preview.OwnerOrgName != "销售部" {
			t.Fatalf("row=%+v", row)
		}
	}
	if catalog.calls != 1 || catalog.validations != 1 {
		t.Fatalf("requests=%d/%d", catalog.calls, catalog.validations)
	}
}
func TestImportLegacyOwnerCannotAssignOtherPersonOrOrganization(t *testing.T) {
	for _, tc := range []struct {
		userID, orgID string
		allowed       bool
	}{{"", "", true}, {"a", "", true}, {"a", "sales", true}, {"b", "sales", false}, {"a", "tech", false}, {"", "tech", false}} {
		row := parsedImportRow{command: importCommand{OwnerUserID: tc.userID, OwnerOrgID: tc.orgID}}
		newImportOwnerResolver(importTestCatalog(), "a").resolveRow(context.Background(), &row, true)
		if tc.allowed == hasImportError(row.issues) {
			t.Fatalf("%+v issues=%+v", tc, row.issues)
		}
		if tc.allowed && (row.command.OwnerUserID != "a" || row.command.OwnerOrgID != "sales") {
			t.Fatalf("owner not current: %+v", row.command)
		}
	}
}
func TestImportActorDirectoryFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing", "unavailable", "membership", "truncated", "wrong tenant", "multiple primary", "no primary"} {
		t.Run(kind, func(t *testing.T) {
			catalog := importTestCatalog()
			switch kind {
			case "missing":
				catalog.users = nil
				catalog.total = 0
			case "unavailable":
				catalog.listErr = errors.New("timeout")
			case "membership":
				catalog.validateErr = ownerdirectory.ErrSelectionInvalid
			case "truncated":
				catalog.total = 3
			case "wrong tenant":
				catalog.tenant = "tenant-b"
			case "multiple primary":
				catalog.users[0].Organizations = append(catalog.users[0].Organizations, ownerdirectory.Organization{ID: "other", IsPrimary: true})
			case "no primary":
				catalog.users[0].Organizations = []ownerdirectory.Organization{{ID: "one"}, {ID: "two"}}
			}
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "a"})
			row := parsedImportRow{}
			newImportOwnerResolver(catalog, "a").resolveRow(ctx, &row, false)
			if !hasImportError(row.issues) || row.command.OwnerUserID != "" {
				t.Fatalf("unsafe row=%+v", row)
			}
		})
	}
}
func TestImportCompleteHeadersAndNewRowMapping(t *testing.T) {
	for _, headers := range [][]string{importHeaders, legacyImportHeaders} {
		cells := make([]safexlsx.Cell, len(headers))
		for i, h := range headers {
			cells[i].Value = h
		}
		if err := validateImportHeader([][]safexlsx.Cell{cells}); err != nil {
			t.Fatal(err)
		}
		cells[5].Value = "负责人姓名"
		if err := validateImportHeader([][]safexlsx.Cell{cells}); err == nil {
			t.Fatal("unpublished header accepted")
		}
	}
	cells := make([]safexlsx.Cell, len(importHeaders))
	for i, v := range customerImportTemplateExample {
		cells[i].Value = v
	}
	row := parseImportRow(2, cells)
	if hasImportError(row.issues) || row.command.ContactName != "张三" || row.command.ContactPhone != "13800138000" || row.command.ContactEmail != "zhangsan@example.com" || row.command.OwnerUserID != "" {
		t.Fatalf("mapping=%+v", row)
	}
}
func TestImportCommitRevalidatesActorPrimaryOrganization(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "a"})
	catalog := importTestCatalog()
	service := &Service{owners: catalog}
	if err := service.validateImportOwner(ctx, "a", "sales"); err != nil {
		t.Fatal(err)
	}
	catalog.users[0].Organizations[0].ID = "new-primary"
	if !errors.Is(service.validateImportOwner(ctx, "a", "sales"), ownerdirectory.ErrSelectionInvalid) {
		t.Fatal("changed primary silently accepted")
	}
	if !errors.Is(service.validateImportOwner(ctx, "b", "sales"), ownerdirectory.ErrSelectionInvalid) {
		t.Fatal("other actor accepted")
	}
	service.owners = nil
	if !errors.Is(service.validateImportOwner(ctx, "a", "sales"), ownerdirectory.ErrUnavailable) {
		t.Fatal("missing directory accepted")
	}
}

type importOwnerRowRepository struct {
	ImportRepository
	updates int
}

func (r *importOwnerRowRepository) UpdateImportRow(context.Context, *ImportRow) error {
	r.updates++
	return nil
}
func TestCommitImportRowRejectsRevokedOwnerBeforeCustomerWrite(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "a"}
	ctx := auth.WithPrincipal(context.Background(), principal)
	catalog := importTestCatalog()
	catalog.validateErr = ownerdirectory.ErrSelectionInvalid
	imports := &importOwnerRowRepository{}
	service := &Service{owners: catalog, imports: imports, codec: profileCodec(t), now: time.Now}
	encoded, err := json.Marshal(importCommand{Name: "安全回归客户", OwnerUserID: "a", OwnerOrgID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := service.codec.Encrypt(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	row := &ImportRow{Status: "READY", CommandCipher: cipher}
	// No customer repository is provided: reaching duplicate lookup or writes
	// would panic and fail this test instead of hiding a revoked-owner bypass.
	if err = service.commitImportRow(ctx, principal, &ImportJob{}, row); err != nil {
		t.Fatal(err)
	}
	if row.Status != "FAILED" || row.ErrorCode != "OWNER_SELECTION_INVALID" || row.CommandCipher != nil || imports.updates != 1 {
		t.Fatalf("row=%+v updates=%d", row, imports.updates)
	}
}
