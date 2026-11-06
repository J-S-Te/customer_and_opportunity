package presale

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin/binding"
)

func TestRequestViewDoesNotExposeSensitiveOrIdempotencyFields(t *testing.T) {
	value := &PresaleRequest{
		BaseModel: BaseModel{ID: 7, TenantID: "tenant-1", Version: 2},
		RequestNo: "TS202607310001", ContactPhoneCipher: []byte("cipher-secret"),
		ContactPhoneMasked: "138****0000", CreateIdempotencyKey: "private-key",
		CreateRequestHash: "private-hash",
	}
	encoded, err := json.Marshal(requestView(value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"cipher-secret", "private-key", "private-hash", "contact_phone_cipher", "create_request_hash"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "138****0000") {
		t.Fatalf("public response must retain masked phone: %s", text)
	}
}

func TestWorklogViewDoesNotExposeRequestHashOrIdempotencyKey(t *testing.T) {
	value := &Worklog{BaseModel: BaseModel{ID: 8}, WorklogNo: "WL1", IdempotencyKey: "private-key", RequestHash: "private-hash"}
	encoded, err := json.Marshal(worklogView(value))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "private-key") || strings.Contains(text, "private-hash") || strings.Contains(text, "request_hash") {
		t.Fatalf("public worklog response leaked internal idempotency data: %s", text)
	}
}

// 描述上限同时存在于 DTO binding 与 validateCreate；HTTP 入口先经过
// requestbody.DecodeJSON 里的 Gin validator，因此这里锁定绑定标签的字符口径。
func TestPresaleDescriptionBindingEnforcesRuneLimit(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	create := CreateRequestInput{
		OpportunityID: 1, Venue: VenueRemote, ContactName: "联系人", ContactPhone: "13800000000",
		Description: strings.Repeat("需", maxDescriptionRunes), ExpectedStart: start,
		ExpectedEnd: start.Add(time.Hour), Urgency: UrgencyNormal,
	}
	if err := binding.Validator.ValidateStruct(&create); err != nil {
		t.Fatalf("description at limit rejected: %v", err)
	}
	create.Description = strings.Repeat("需", maxDescriptionRunes+1)
	if err := binding.Validator.ValidateStruct(&create); err == nil {
		t.Fatal("create must reject description above the limit")
	}

	reopen := ReopenRequestInput{
		Venue: VenueRemote, ContactName: "联系人", ContactPhone: "13800000000",
		Description: strings.Repeat("需", maxDescriptionRunes), ExpectedStart: start,
		ExpectedEnd: start.Add(time.Hour), Urgency: UrgencyNormal,
	}
	if err := binding.Validator.ValidateStruct(&reopen); err != nil {
		t.Fatalf("reopen description at limit rejected: %v", err)
	}
	reopen.Description = strings.Repeat("需", maxDescriptionRunes+1)
	if err := binding.Validator.ValidateStruct(&reopen); err == nil {
		t.Fatal("reopen must reject description above the limit")
	}
}
