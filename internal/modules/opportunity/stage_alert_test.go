package opportunity

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

func TestTimedStagesExcludeTerminalStages(t *testing.T) {
	t.Parallel()
	if len(timedStages()) != 5 {
		t.Fatalf("timed stages=%v", timedStages())
	}
	for _, stage := range []string{StageSigned, StageFailed, "unknown"} {
		if isTimedStage(stage) {
			t.Fatalf("stage %q must not have a stay threshold", stage)
		}
	}
}

func TestStageAlertListScopeMatchesOpportunityDataScope(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "gorm:gorm@tcp(127.0.0.1:9910)/gorm?charset=utf8mb4&parseTime=True&loc=Local", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	for _, test := range []struct {
		name      string
		principal auth.Principal
		want      string
	}{
		{name: "self", principal: auth.Principal{UserID: "owner-a", ScopeMode: auth.ScopeSelf}, want: "owner_user_id"},
		{name: "org", principal: auth.Principal{ScopeMode: auth.ScopeOrg, OrganizationIDs: []string{"org-a"}}, want: "owner_org_id"},
		{name: "empty org", principal: auth.Principal{ScopeMode: auth.ScopeOrg}, want: "1=0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rows []StageAlertResponse
			statement := scopeStageAlertOpportunities(db.Table("crm_opportunities o"), test.principal).Find(&rows).Statement
			if !strings.Contains(statement.SQL.String(), test.want) {
				t.Fatalf("missing %q in %q", test.want, statement.SQL.String())
			}
		})
	}
}

func TestRequireAlertPrincipalChecksAuthenticationAndPermission(t *testing.T) {
	t.Parallel()
	if _, err := requireAlertPrincipal(context.Background(), "opportunity.read"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("unauthenticated error=%v", err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "user-a", Permissions: map[string]struct{}{}})
	if _, err := requireAlertPrincipal(ctx, "opportunity.read"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("permission error=%v", err)
	}
	ctx = auth.WithPrincipal(context.Background(), auth.Principal{TenantID: "tenant-a", UserID: "user-a", Permissions: map[string]struct{}{"opportunity.read": {}}})
	principal, err := requireAlertPrincipal(ctx, "opportunity.read")
	if err != nil || principal.UserID != "user-a" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

func TestStageAlertRuleResponseDoesNotExposeTenantOrInternalID(t *testing.T) {
	t.Parallel()
	rule := StageAlertRule{Stage: StageQuotation, ThresholdHours: 48, Enabled: true, ConfigVersion: 3}
	rule.ID, rule.TenantID, rule.Version = 99, "tenant-secret", 4
	response := stageAlertRuleResponse(rule)
	if response.Stage != StageQuotation || response.ConfigVersion != 3 || response.Version != 4 {
		t.Fatalf("response=%#v", response)
	}
}
