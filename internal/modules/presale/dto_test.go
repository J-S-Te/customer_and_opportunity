package presale

import (
	"encoding/json"
	"strings"
	"testing"
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
