package presale

import (
	"strings"
	"testing"
	"time"
)

func TestValidateCreateRejectsOversizedText(t *testing.T) {
	base := CreateRequestInput{
		OpportunityID: 1, Venue: VenueRemote, ContactName: "联系人", ContactPhone: "13800000000",
		Description: "需求", ExpectedStart: time.Now(), ExpectedEnd: time.Now().Add(time.Hour), Urgency: UrgencyNormal,
	}
	if err := validateCreate(base); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	cases := []CreateRequestInput{
		func() CreateRequestInput { item := base; item.ServiceAddress = strings.Repeat("址", 501); return item }(),
		func() CreateRequestInput { item := base; item.ContactName = strings.Repeat("人", 129); return item }(),
		func() CreateRequestInput { item := base; item.ContactPhone = strings.Repeat("1", 65); return item }(),
		func() CreateRequestInput { item := base; item.Description = strings.Repeat("需", 60001); return item }(),
	}
	for index, input := range cases {
		if err := validateCreate(input); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
}
