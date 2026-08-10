package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func TestLocalAttachmentFlowScansBeforeDownload(t *testing.T) {
	store, err := NewLocalAttachmentObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &attachmentRepoStub{values: map[string]*Attachment{}}
	service := NewAttachmentService(repo, attachmentOpportunityRepo{}, &countingAuditWriter{}, store, NewCodeAttachmentScanner(store), 0)
	content := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\n%%EOF")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	created, err := service.CreateUpload(attachmentContext("tenant-a"), 7, AttachmentCreateRequest{FileName: "safe.pdf", SizeBytes: uint64(len(content)), MIMEType: "application/pdf", SHA256: digest, IdempotencyKey: "local-create"})
	if err != nil || created.UploadMode != "INTERNAL" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	if _, err = service.UploadContent(attachmentContext("tenant-a"), 7, created.Attachment.ID, bytes.NewReader(content), "application/pdf", int64(len(content))); err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteUpload(attachmentContext("tenant-a"), 7, created.Attachment.ID, AttachmentCompleteRequest{Version: created.Attachment.Version, IdempotencyKey: "local-complete"})
	if err != nil || completed.ScanStatus != AttachmentClean || completed.ScannedAt == nil {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	download, err := service.Download(attachmentContext("tenant-a"), 7, created.Attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Reader.Close()
	got, _ := io.ReadAll(download.Reader)
	if !bytes.Equal(got, content) {
		t.Fatalf("download mismatch: %q", got)
	}
}

func TestCodeAttachmentScannerRejectsActiveAndDisguisedContent(t *testing.T) {
	cases := []struct {
		name, media string
		content     []byte
	}{
		{name: "pdf javascript", media: "application/pdf", content: []byte("%PDF-1.4\n/JavaScript /OpenAction\n%%EOF")},
		{name: "executable as image", media: "image/png", content: []byte("MZ executable")},
		{name: "eicar", media: "application/pdf", content: []byte("%PDF-1.4\nX5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*\n%%EOF")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := scanAttachmentBytes(test.content, test.media); !errors.Is(err, ErrAttachmentRejected) {
				t.Fatalf("scan error=%v", err)
			}
		})
	}
}

func TestLocalAttachmentUploadRejectsDigestMismatch(t *testing.T) {
	store, err := NewLocalAttachmentObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutVerified(context.Background(), "crm/opportunities/t/7/file", bytes.NewReader([]byte("unsafe")), 6, testAttachmentSHA, "application/pdf")
	if !errors.Is(err, ErrAttachmentInvalid) {
		t.Fatalf("error=%v", err)
	}
}
