package filegatewayclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayRequestsCannotFollowRedirectToAnotherOrigin(t *testing.T) {
	forwarded := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		_, _ = io.WriteString(w, `{"data":{"upload_id":"foreign","file_id":"foreign","status":"READY"}}`)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+r.URL.Path)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := New(origin.URL, origin.Client(), func(context.Context) (string, error) { return "scoped-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ResolveV2Upload(context.Background(), V2UploadInput{RequestID: "request-1", Purpose: "crm.customer.import", Name: "customers.xlsx", MediaType: "application/xlsx", ResourceType: "CUSTOMER_IMPORT", ResourceID: "job-1", BindingType: "SOURCE", SizeBytes: 7, SHA256: strings.Repeat("a", 64)})
	if err == nil || forwarded != 0 {
		t.Fatalf("gateway escaped configured origin: error=%v forwarded=%d", err, forwarded)
	}
}

func TestUploadV2RecoversCompletedUploadAfterResponseLoss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/upload-sessions" {
			t.Errorf("completed upload must not acquire another ticket or resend content: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"upload_id":"upload-1","file_id":"file-1","status":"READY"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), func(context.Context) (string, error) { return "token", nil })
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.UploadV2(context.Background(), V2UploadInput{
		RequestID: "source-1", Purpose: "crm.customer.import", Name: "customers.xlsx",
		MediaType:    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ResourceType: "CUSTOMER_IMPORT", ResourceID: "job-1", BindingType: "SOURCE", Content: strings.NewReader("content"),
	})
	if err != nil || receipt.FileID != "file-1" {
		t.Fatalf("completed upload recovery receipt=%+v error=%v", receipt, err)
	}
}
