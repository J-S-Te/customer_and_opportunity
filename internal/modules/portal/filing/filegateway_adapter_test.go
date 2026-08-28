package filing

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
	body := []byte("filing material")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err = store.PutVerified(context.Background(), "portal/filings/t/f/m", bytes.NewReader(body), uint64(len(body)), digest, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	// 相同重试只验证既有临时对象，不覆盖内容；元数据不同的重复写入必须拒绝。
	if err = store.PutVerified(context.Background(), "portal/filings/t/f/m", bytes.NewReader(body), uint64(len(body)), digest, "application/pdf"); err != nil {
		t.Fatalf("exact content replay: %v", err)
	}
	other := sha256.Sum256([]byte("other"))
	if err = store.PutVerified(context.Background(), "portal/filings/t/f/m", bytes.NewReader(body), uint64(len(body)), hex.EncodeToString(other[:]), "application/pdf"); err != ErrMaterialContentInvalid {
		t.Fatalf("different digest replay error=%v", err)
	}
	meta, err := store.Finalize(context.Background(), "portal/filings/t/f/m")
	if err != nil || meta.ObjectVersion != digest {
		t.Fatalf("meta=%#v err=%v", meta, err)
	}
	r, err := store.OpenVerified(context.Background(), "portal/filings/t/f/m", digest, digest, uint64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	actual, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("actual=%q err=%v", actual, err)
	}
}

func TestLocalMaterialScannerProducesExplicitImmediateStatus(t *testing.T) {
	client, err := filegateway.NewLocalClient(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewLocalFileGatewayStore(client)
	content := []byte("%PDF-1.4\n%%EOF")
	digestValue := sha256.Sum256(content)
	digest := hex.EncodeToString(digestValue[:])
	key := "portal/filings/t/f/safe"
	if err = store.PutVerified(context.Background(), key, bytes.NewReader(content), uint64(len(content)), digest, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Finalize(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	scanner := NewLocalMaterialScanner(store)
	reference, err := scanner.Submit(context.Background(), "material-safe", key, metadata.ObjectVersion, digest, uint64(len(content)), "application/pdf")
	status, complete := scanner.ImmediateStatus(reference)
	if err != nil || !complete || status != MaterialClean {
		t.Fatalf("reference=%q status=%q complete=%v err=%v", reference, status, complete, err)
	}
}
