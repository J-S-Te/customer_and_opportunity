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
	// 描述上限与 DTO binding 标签同为 5000 字符：恰好 5000 必须放行，5001 必须拒绝。
	atLimit := base
	atLimit.Description = strings.Repeat("需", maxDescriptionRunes)
	if err := validateCreate(atLimit); err != nil {
		t.Fatalf("description at limit rejected: %v", err)
	}
	cases := []CreateRequestInput{
		func() CreateRequestInput { item := base; item.ServiceAddress = strings.Repeat("址", 501); return item }(),
		func() CreateRequestInput { item := base; item.ContactName = strings.Repeat("人", 129); return item }(),
		func() CreateRequestInput { item := base; item.ContactPhone = strings.Repeat("1", 65); return item }(),
		func() CreateRequestInput {
			item := base
			item.Description = strings.Repeat("需", maxDescriptionRunes+1)
			return item
		}(),
	}
	for index, input := range cases {
		if err := validateCreate(input); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
}
