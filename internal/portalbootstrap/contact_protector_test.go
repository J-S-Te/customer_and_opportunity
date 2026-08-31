package portalbootstrap

import (
	"context"
	"strings"
	"testing"
)

func TestContactProtectorStoresNonNilCipherForEmptyContact(t *testing.T) {
	codec, err := NewAEADCodec([]byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, masked, err := (contactProtector{codec: codec}).Encrypt(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("empty contact must still produce a non-empty ciphertext for the NOT NULL column")
	}
	if masked != "" {
		t.Fatalf("empty contact mask = %q, want empty", masked)
	}
}
