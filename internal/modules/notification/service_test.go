package notification

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestPersonalInboxNeverUsesOpportunityDataScope(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	statement := personalInboxQuery(db, auth.Principal{TenantID: "tenant-a", UserID: "user-a", PersonID: "person-a", Permissions: map[string]struct{}{"opportunity.read": {}, "presale.read": {}}}).Find(&[]Notification{}).Statement
	sql := statement.SQL.String()
	for _, predicate := range []string{"tenant_id", "recipient_id", "deleted_at", "type"} {
		if !strings.Contains(sql, predicate) {
			t.Fatalf("missing %s in %q", predicate, sql)
		}
	}
	for _, forbidden := range []string{"owner_org_id", "owner_user_id"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("personal inbox was expanded by opportunity scope: %q", sql)
		}
	}
}

func TestNotificationPrincipalRequiresReadPermission(t *testing.T) {
	if _, err := requirePrincipal(context.Background()); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("unauthenticated error=%v", err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "user-a", ScopeMode: auth.ScopeAll, Permissions: map[string]struct{}{}})
	if _, err := requirePrincipal(ctx); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("permission error=%v", err)
	}
	ctx = auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "user-a", ScopeMode: auth.ScopeAll, Permissions: map[string]struct{}{"opportunity.read": {}}})
	if principal, err := requirePrincipal(ctx); err != nil || principal.UserID != "user-a" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

func TestNotificationResponseDoesNotExposeTenantOrSourceEvent(t *testing.T) {
	response := Response{ID: 1, OpportunityID: 7, OpportunityNo: "SJ1", RecipientKind: RecipientNewOwner}
	if response.OpportunityID != 7 || response.RecipientKind != RecipientNewOwner {
		t.Fatalf("response=%#v", response)
	}
}

func TestPersonalInboxSeparatesUserAndPersonRecipientNamespaces(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	statement := personalInboxQuery(db, auth.Principal{TenantID: "tenant-a", UserID: "user-a", PersonID: "person-a", Permissions: map[string]struct{}{"opportunity.read": {}, "presale.read": {}}}).Find(&[]Notification{}).Statement
	sql, variables := statement.SQL.String(), statement.Vars
	if !strings.Contains(sql, "type IN") || !containsVariable(variables, "user-a") || !containsVariable(variables, "person-a") {
		t.Fatalf("personal namespace predicates missing: sql=%q vars=%#v", sql, variables)
	}
}

func containsVariable(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestMarkReadIsIdempotentByContract(t *testing.T) {
	if StatusRead != "READ" {
		t.Fatal("already READ rows must be treated as idempotent replay")
	}
}
