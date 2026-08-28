package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
)

type attachmentGatewayMigrationStub struct {
	err      error
	required bool
	inputs   []AttachmentGatewayMigrationInput
}

func (stub *attachmentGatewayMigrationStub) Required() bool { return stub.required }
func (stub *attachmentGatewayMigrationStub) Migrate(_ context.Context, input AttachmentGatewayMigrationInput) error {
	stub.inputs = append(stub.inputs, input)
	return stub.err
}

func TestAttachmentUploadDualAndRequiredMigrationModes(t *testing.T) {
	content := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\n%%EOF")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	for _, test := range []struct {
		name     string
		required bool
		wantErr  bool
	}{{"dual", false, false}, {"required", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewLocalAttachmentObjectStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo := &attachmentRepoStub{values: map[string]*Attachment{}}
			migration := &attachmentGatewayMigrationStub{err: errors.New("gateway unavailable"), required: test.required}
			service := NewAttachmentService(repo, attachmentOpportunityRepo{}, &countingAuditWriter{}, store, NewCodeAttachmentScanner(store), 0).UseFileGatewayMigration(migration)
			created, err := service.CreateUpload(attachmentContext("tenant-a"), 7, AttachmentCreateRequest{FileName: "safe.pdf", SizeBytes: uint64(len(content)), MIMEType: "application/pdf", SHA256: digest, IdempotencyKey: "migration-key"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.UploadContent(attachmentContext("tenant-a"), 7, created.Attachment.ID, bytes.NewReader(content), "application/pdf", int64(len(content)))
			if (err != nil) != test.wantErr || len(migration.inputs) != 1 {
				t.Fatalf("err=%v calls=%d", err, len(migration.inputs))
			}
			if migration.inputs[0].AttachmentID != created.Attachment.ID || migration.inputs[0].OpportunityID != 7 || !bytes.Equal(migration.inputs[0].Content, content) {
				t.Fatalf("unexpected migration input %#v", migration.inputs[0])
			}
		})
	}
}

func TestHTTPAttachmentGatewayMigrationUploadsAndBindsWithStableKeys(t *testing.T) {
	var uploadKey, bindKey string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("Idempotency-Key") == "" || request.Header.Get("Idempotency-Key") != request.Header.Get("X-Request-ID") {
			t.Errorf("invalid gateway headers %#v", request.Header)
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/files":
			uploadKey = request.Header.Get("Idempotency-Key")
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"data":{"file_id":"file-1"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/files/file-1/bindings":
			bindKey = request.Header.Get("Idempotency-Key")
			writer.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected gateway route %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := filegatewayclient.New(server.URL, server.Client(), func(context.Context) (string, error) { return "token", nil })
	if err != nil {
		t.Fatal(err)
	}
	migration, err := NewHTTPAttachmentGatewayMigration(client, "crm-app", AttachmentGatewayDual)
	if err != nil {
		t.Fatal(err)
	}
	input := AttachmentGatewayMigrationInput{TenantID: "tenant-a", OpportunityID: 7, AttachmentID: "attachment-a", FileName: "proof.pdf", MIMEType: "application/pdf", SHA256: strings.Repeat("a", 64), IdempotencyKey: "create-key", Content: []byte("content")}
	if err = migration.Migrate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if uploadKey == "" || bindKey == "" || uploadKey == bindKey || uploadKey != attachmentGatewayRequestKey("upload", input) || bindKey != attachmentGatewayRequestKey("bind", input) {
		t.Fatalf("upload=%q bind=%q", uploadKey, bindKey)
	}
}
