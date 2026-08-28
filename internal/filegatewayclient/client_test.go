package filegatewayclient

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadPreservesApplicationIdentityAndRequestID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/files" || request.Header.Get("Authorization") != "Bearer machine-token" || request.Header.Get("X-Request-ID") != "request-1" || request.Header.Get("Idempotency-Key") != "request-1" {
			t.Fatalf("unexpected request path=%s headers=%v", request.URL.Path, request.Header)
		}
		if err := request.ParseMultipartForm(21 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, _ := io.ReadAll(file)
		if request.FormValue("application_id") != "app-1" || header.Filename != "材料.pdf" || !bytes.Equal(content, []byte("content")) {
			t.Fatalf("unexpected multipart form application=%q filename=%q content=%q", request.FormValue("application_id"), header.Filename, content)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"data":{"file_id":"file-1"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), func(context.Context) (string, error) { return "machine-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := client.Upload(context.Background(), "request-1", "app-1", "CONFIDENTIAL", "材料.pdf", "application/pdf", strings.NewReader("content"))
	if err != nil || fileID != "file-1" {
		t.Fatalf("fileID=%q err=%v", fileID, err)
	}
}
