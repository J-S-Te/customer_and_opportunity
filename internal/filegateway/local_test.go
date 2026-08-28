package filegateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestLocalClientRoundTripAndVersionBinding(t *testing.T) {
	client, err := NewLocalClient(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("trusted report")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err = client.PutVerified(context.Background(), "portal/reports/tenant-1/report-1", bytes.NewReader(body), uint64(len(body)), digest, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	meta, err := client.Finalize(context.Background(), "portal/reports/tenant-1/report-1")
	if err != nil || meta.ObjectVersion != digest {
		t.Fatalf("Finalize() = %#v, %v", meta, err)
	}
	if _, err = client.OpenVerified(context.Background(), "portal/reports/tenant-1/report-1", "wrong", digest, uint64(len(body))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong version error = %v", err)
	}
	reader, err := client.OpenVerified(context.Background(), "portal/reports/tenant-1/report-1", digest, digest, uint64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	actual, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("read = %q, %v", actual, err)
	}
}

func TestLocalClientRejectsPathEscapeAndDigestMismatch(t *testing.T) {
	client, err := NewLocalClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = client.PutVerified(context.Background(), "../escape", bytes.NewReader([]byte("x")), 1, "", "text/plain"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("path/digest error = %v", err)
	}
	if err = client.PutVerified(context.Background(), "safe/object", bytes.NewReader([]byte("x")), 1, "00", "text/plain"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest error = %v", err)
	}
}
