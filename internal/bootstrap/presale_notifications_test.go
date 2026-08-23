package bootstrap

import (
	"regexp"
	"testing"
	"unicode/utf8"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func TestPresaleNotificationSourceEventIDMatchesPlatformContract(t *testing.T) {
	notification := presale.WorkflowNotification{
		TenantID:    "01J00000000000000000000000",
		Type:        "PRESALE_APPROVAL_PENDING",
		RequestNo:   "TS202608230002",
		RecipientID: "01KYRT9B3KYNTJG6P4X9078SJB",
	}
	got := presaleNotificationSourceEventID(notification)

	platformCodePattern := regexp.MustCompile(`^[A-Z][A-Z0-9_:-]{0,127}$`)
	if !platformCodePattern.MatchString(got) {
		t.Fatalf("source event id %q does not satisfy the platform code contract", got)
	}
	if utf8.RuneCountInString(got) > 128 {
		t.Fatalf("source event id length = %d, want <= 128", utf8.RuneCountInString(got))
	}
	canonical := "customer_and_opportunity:dev:" + got
	if len(canonical) > 128 {
		t.Fatalf("canonical idempotency key length = %d, want <= 128", len(canonical))
	}
	notification.AssignmentID = 42
	if assigned := presaleNotificationSourceEventID(notification); assigned == got {
		t.Fatal("assignment-scoped notification must have a distinct source event id")
	}
}
