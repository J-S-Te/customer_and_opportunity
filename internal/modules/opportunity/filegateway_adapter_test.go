package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegateway"
)

func TestLocalFileGatewayStoreRoundTrip(t *testing.T) {
	client, err := filegateway.NewLocalClient(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewLocalFileGatewayStore(client)
	body := []byte("opportunity attachment")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err = store.PutVerified(context.Background(), "crm/opportunities/t/o/a", bytes.NewReader(body), uint64(len(body)), digest, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Finalize(context.Background(), "crm/opportunities/t/o/a")
	if err != nil || meta.ObjectVersion != digest {
		t.Fatalf("meta=%#v err=%v", meta, err)
	}
	r, err := store.OpenVerified(context.Background(), "crm/opportunities/t/o/a", digest, digest, uint64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	actual, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("actual=%q err=%v", actual, err)
	}
}
