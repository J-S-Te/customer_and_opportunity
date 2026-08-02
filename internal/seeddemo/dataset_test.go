package seeddemo

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var validStages = map[string]struct{}{
	"初步接触": {}, "需求沟通": {}, "方案制定": {}, "报价": {}, "投标": {}, "已签约": {}, "失败": {},
}

func TestCustomerDatasetReferencesKnownPeople(t *testing.T) {
	people := People()
	seenKeys := make(map[string]struct{})
	for _, item := range customers() {
		if _, ok := people[item.OwnerKey]; !ok {
			t.Fatalf("customer %s references unknown owner %q", item.Key, item.OwnerKey)
		}
		if item.Name == "" || item.CustomerType == "" || item.Industry == "" || item.Region == "" || len(item.Contacts) == 0 {
			t.Fatalf("customer %s has incomplete master data", item.Key)
		}
		if _, duplicate := seenKeys[item.Key]; duplicate {
			t.Fatalf("duplicate customer key %s", item.Key)
		}
		seenKeys[item.Key] = struct{}{}
		hasRegistration := false
		for _, contact := range item.Contacts {
			if contact.Name == "" || contact.Phone == "" {
				t.Fatalf("customer %s contact %+v is incomplete", item.Key, contact)
			}
			if contact.Registration {
				hasRegistration = true
			}
		}
		if !hasRegistration {
			t.Fatalf("customer %s has no registration contact", item.Key)
		}
		for _, system := range item.Systems {
			if !strings.HasPrefix(system.ProtectionLevel, "LEVEL_") || system.FilingStatus != "FILED" {
				t.Fatalf("customer %s system %+v uses invalid protection/filing values", item.Key, system)
			}
			if _, err := time.Parse("2006-01-02", system.GradingDate); err != nil {
				t.Fatalf("customer %s system grading date invalid: %v", item.Key, err)
			}
		}
		for _, followup := range item.Followups {
			if _, err := time.Parse(time.RFC3339, followup.FollowedAt); err != nil {
				t.Fatalf("customer %s followup time invalid: %v", item.Key, err)
			}
		}
	}
}

func TestOpportunityDatasetReferencesCustomersAndKnownPeople(t *testing.T) {
	people := People()
	customersByKey := make(map[string]customerSeed)
	for _, item := range customers() {
		customersByKey[item.Key] = item
	}
	seenKeys := make(map[string]struct{})
	for _, item := range opportunities() {
		if _, ok := customersByKey[item.CustomerKey]; !ok {
			t.Fatalf("opportunity %s references unknown customer %s", item.Key, item.CustomerKey)
		}
		if _, ok := people[item.OwnerKey]; !ok {
			t.Fatalf("opportunity %s references unknown owner %q", item.Key, item.OwnerKey)
		}
		if _, duplicate := seenKeys[item.Key]; duplicate {
			t.Fatalf("duplicate opportunity key %s", item.Key)
		}
		seenKeys[item.Key] = struct{}{}
		amount, err := decimal.NewFromString(item.ExpectedAmount)
		if err != nil || !amount.IsPositive() {
			t.Fatalf("opportunity %s expected amount %q is invalid", item.Key, item.ExpectedAmount)
		}
		if _, err := time.Parse("2006-01-02", item.ExpectedSignDate); err != nil {
			t.Fatalf("opportunity %s expected sign date invalid: %v", item.Key, err)
		}
		if _, ok := validStages[item.Stage]; !ok {
			t.Fatalf("opportunity %s has invalid stage %q", item.Key, item.Stage)
		}
		for _, member := range item.Members {
			if _, ok := people[member.UserKey]; !ok {
				t.Fatalf("opportunity %s references unknown member %q", item.Key, member.UserKey)
			}
			if member.Role == "" {
				t.Fatalf("opportunity %s member role is empty", item.Key)
			}
		}
		if item.Terminal == nil {
			continue
		}
		terminal := item.Terminal
		if _, ok := validStages[terminal.Stage]; !ok || terminal.FromStage == "" {
			t.Fatalf("opportunity %s terminal transition is incomplete: %+v", item.Key, terminal)
		}
		if terminal.Stage == "已签约" {
			if terminal.PendingType == "NONE" && terminal.ContractRef == "" {
				t.Fatalf("opportunity %s signed without contract reference or pending marker", item.Key)
			}
			if terminal.PendingType != "NONE" && terminal.PendingType != "CONTRACT" {
				t.Fatalf("opportunity %s invalid signed pending type %q", item.Key, terminal.PendingType)
			}
		}
		if terminal.Stage == "失败" {
			if terminal.PendingType == "NONE" && terminal.LostReason == "" {
				t.Fatalf("opportunity %s failed without lost reason or pending marker", item.Key)
			}
			if terminal.PendingType != "NONE" && terminal.PendingType != "LOST_REASON" {
				t.Fatalf("opportunity %s invalid failed pending type %q", item.Key, terminal.PendingType)
			}
		}
	}
}

func TestOwnerOrganizationMatchesPersonMapping(t *testing.T) {
	people := People()
	for _, item := range customers() {
		person := people[item.OwnerKey]
		if person.OrgID == "" || person.Sub == "" {
			t.Fatalf("customer %s owner %q has incomplete person mapping", item.Key, item.OwnerKey)
		}
	}
	for _, item := range opportunities() {
		person := people[item.OwnerKey]
		if person.OrgID == "" || person.Sub == "" {
			t.Fatalf("opportunity %s owner %q has incomplete person mapping", item.Key, item.OwnerKey)
		}
	}
}

func TestNormalizeNameMatchesProductionContract(t *testing.T) {
	if got := normalizeName(" 华兴证券 股份有限公司 "); got != "华兴证券股份有限公司" {
		t.Fatalf("normalized name=%q", got)
	}
	if maskEmail("chenzhiyuan@huaxing.example.com") != "c***@huaxing.example.com" {
		t.Fatalf("masked email=%q", maskEmail("chenzhiyuan@huaxing.example.com"))
	}
}
