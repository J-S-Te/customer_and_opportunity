package customer

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func customerDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestValidateListQueryRejectsUnknownValuesAndInvalidRanges(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	tests := []ListQuery{
		{QuickFilter: "HIGH_VALUE"}, {SortBy: "tenant_id"}, {SortOrder: "sideways"},
		{CreatedFrom: ptrTime(now), CreatedTo: ptrTime(now)},
		{LastFollowupFrom: ptrTime(now.Add(time.Hour)), LastFollowupTo: ptrTime(now)},
	}
	for _, input := range tests {
		if _, err := validateListQuery(input, now); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("input=%#v err=%v", input, err)
		}
	}
}

func TestValidateListQueryCanonicalizesWhitelistAndUsesThirtyDayNewWindow(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	result, err := validateListQuery(ListQuery{QuickFilter: " new ", SortBy: " LAST_FOLLOWUP_AT ", SortOrder: " ASC "}, now)
	if err != nil || result.QuickFilter != QuickFilterNew || result.SortBy != "last_followup_at" || result.SortOrder != "asc" || !result.Now.Equal(now) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := result.Now.AddDate(0, 0, -30); !got.Equal(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("window start=%s", got)
	}
}

func TestScopedCustomerSelfAndOwnerFilterCannotExpandVisibility(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "owner-self", ScopeMode: auth.ScopeSelf}
	statement := scopedCustomer(customerDryRunDB(t).Model(&Customer{}), principal).Where("owner_user_id = ?", "other-owner").Find(&[]Customer{}).Statement
	sql := statement.SQL.String()
	for _, fragment := range []string{"crm_customers.tenant_id", "crm_customers.owner_user_id", "owner_user_id = ?"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing %q in %q", fragment, sql)
		}
	}
	if len(statement.Vars) < 3 || statement.Vars[0] != "tenant-a" || statement.Vars[1] != "owner-self" || statement.Vars[2] != "other-owner" {
		t.Fatalf("vars=%#v", statement.Vars)
	}
}

func TestCustomerListSQLContainsQuickFiltersAndWhitelistedStableSort(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", UserID: "sales-a", ScopeMode: auth.ScopeSelf}
	for _, test := range []struct{ quick, fragment string }{
		{QuickFilterNew, "crm_customers.created_at >="}, {QuickFilterWon, "current_stage = '已签约'"}, {QuickFilterFollowupDue, "next_follow_at <="},
	} {
		db := buildCustomerListQuery(customerDryRunDB(t), principal, ListQuery{QuickFilter: test.quick, Now: time.Now().UTC()})
		statement := db.Order("customer_opportunity_summary.opportunity_amount_sum ASC").Order("crm_customers.id ASC").Find(&[]Customer{}).Statement
		sql := statement.SQL.String()
		if !strings.Contains(sql, test.fragment) || !strings.Contains(sql, "customer_opportunity_summary.opportunity_amount_sum ASC") || !strings.Contains(sql, "crm_customers.id ASC") {
			t.Fatalf("quick=%s sql=%q", test.quick, sql)
		}
		if !strings.Contains(sql, "crm_customers.owner_user_id") {
			t.Fatalf("SELF scope missing: %q", sql)
		}
		tenantBindings := 0
		for _, value := range statement.Vars {
			if value == "tenant-a" {
				tenantBindings++
			}
		}
		if tenantBindings != 3 {
			t.Fatalf("outer scope and both aggregate subqueries must be tenant-bound: vars=%#v", statement.Vars)
		}
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestParseOptionalTimeRequiresRFC3339(t *testing.T) {
	if value, err := parseOptionalTime("2026-08-01"); err == nil || value != nil {
		t.Fatalf("value=%v err=%v", value, err)
	}
	value, err := parseOptionalTime("2026-08-01T08:30:00+08:00")
	if err != nil || value.Format(time.RFC3339) != "2026-08-01T00:30:00Z" {
		t.Fatalf("value=%v err=%v", value, err)
	}
}
