package filegatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestUploadV2DecodesSnakeCaseSessionAndTicketResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.Header.Get("Authorization") != "Bearer machine-token" {
			t.Errorf("unexpected authorization header %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/upload-sessions":
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode session request: %v", err)
			}
			if payload["purpose"] != "crm.customer.import" || payload["resource_id"] != "job-1" {
				t.Errorf("unexpected session request payload: %#v", payload)
			}
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"data":{"upload_id":"upload-1","file_id":"file-1","status":"CREATED","upload_url":"/file-gateway/api/v2/upload-sessions/upload-1/content"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/upload-sessions/upload-1/tickets":
			_, _ = writer.Write([]byte(`{"data":{"ticket":"one-time-ticket","upload_url":"/file-gateway/api/v2/upload-sessions/upload-1/content","expires_at":"2026-09-28T09:00:00Z"}}`))
		case request.Method == http.MethodPut && request.URL.Path == "/api/v2/upload-sessions/upload-1/content":
			if request.Header.Get("Authorization") != "UploadTicket one-time-ticket" {
				t.Errorf("unexpected upload ticket header %q", request.Header.Get("Authorization"))
			}
			content, err := io.ReadAll(request.Body)
			if err != nil || string(content) != "xlsx-content" {
				t.Errorf("unexpected uploaded content %q, err=%v", content, err)
			}
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"data":{"binding_id":"binding-1","status":"READY"}}`))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, server.Client(), func(context.Context) (string, error) { return "machine-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.UploadV2(context.Background(), V2UploadInput{
		RequestID: "crm-customer-import-job-1", Purpose: "crm.customer.import", Classification: "INTERNAL",
		ActorUserID: "user-1", Name: "customers.xlsx", MediaType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ResourceType: "CUSTOMER_IMPORT", ResourceID: "job-1", BindingType: "SOURCE", DisplayName: "customers.xlsx",
		Content: strings.NewReader("xlsx-content"),
	})
	if err != nil {
		t.Fatalf("UploadV2 returned error: %v", err)
	}
	if receipt.FileID != "file-1" || receipt.BindingID != "binding-1" || receipt.SizeBytes != uint64(len("xlsx-content")) || receipt.SHA256 == "" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
}
