package presaleworker

import (
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/presale"
)

func TestOutboxRetryAtSixRetriesThenDeadLetter(t *testing.T) {
	now := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	wants := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
	for i, want := range wants {
		next, ok := outboxRetryAt(now, uint8(i+1))
		if !ok || next == nil || !next.Equal(now.Add(want)) {
			t.Fatalf("attempt %d next=%v ok=%v, want %v", i+1, next, ok, now.Add(want))
		}
	}
	if next, ok := outboxRetryAt(now, 7); ok || next != nil {
		t.Fatalf("seventh failure must dead-letter, got next=%v ok=%v", next, ok)
	}
	status, next := outboxFailurePlan(now, 7)
	if status != "DEAD_LETTER" || next != nil {
		t.Fatalf("seventh failure plan=%s/%v", status, next)
	}
}

func TestDomainAndOutboxUseSameRetryBoundary(t *testing.T) {
	now := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	for attempt := uint8(1); attempt <= 7; attempt++ {
		domainNext, domainOK := presale.DeliveryRetryAt(now, attempt)
		outboxNext, outboxOK := outboxRetryAt(now, attempt)
		if domainOK != outboxOK || (domainNext != nil && !domainNext.Equal(*outboxNext)) {
			t.Fatalf("attempt %d policies differ: domain=%v/%v outbox=%v/%v", attempt, domainNext, domainOK, outboxNext, outboxOK)
		}
	}
}

func TestSanitizeHeaderRejectsUnsafeValues(t *testing.T) {
	if got := sanitizeHeader("request-123_OK"); got != "request-123_OK" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeHeader("secret\r\nAuthorization: bearer"); got != "" {
		t.Fatalf("unsafe header was retained: %q", got)
	}
}
