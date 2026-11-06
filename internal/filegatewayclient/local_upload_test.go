package filegatewayclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLocalUploadCompletionRequiresExactReadyReceipt 验证真实v2四字段完成回执，不接受200占位。
// 参数：t为HTTP协议测试上下文。
// 返回值：无；身份不匹配、非READY、缺绑定或损坏响应被接受时失败。
func TestLocalUploadCompletionRequiresExactReadyReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"valid", `{"data":{"upload_id":"upload-1","file_id":"file-1","binding_id":"binding-1","status":"READY"}}`, 201, true},
		{"wrong-file", `{"data":{"upload_id":"upload-1","file_id":"other","binding_id":"binding-1","status":"READY"}}`, 201, false},
		{"missing-binding", `{"data":{"upload_id":"upload-1","file_id":"file-1","status":"READY"}}`, 201, false},
		{"unknown-status", `{"data":{"upload_id":"upload-1","file_id":"file-1","binding_id":"binding-1","status":"ACCEPTED"}}`, 201, false},
		{"placeholder-200", `{"data":{"binding_id":"binding-1"}}`, 200, false},
		{"trailing", `{"data":{"upload_id":"upload-1","file_id":"file-1","binding_id":"binding-1","status":"READY"}} {}`, 201, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /api/v2/upload-sessions":
					_, _ = io.WriteString(w, `{"data":{"upload_id":"upload-1","file_id":"file-1","status":"CREATED","upload_url":"/file-gateway/api/v2/upload-sessions/upload-1/content"}}`)
				case "POST /api/v2/upload-sessions/upload-1/tickets":
					_, _ = io.WriteString(w, `{"data":{"ticket":"one-use-ticket","upload_url":"/file-gateway/api/v2/upload-sessions/upload-1/content","expires_at":"2030-01-01T00:00:00Z"}}`)
				case "PUT /api/v2/upload-sessions/upload-1/content":
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				default:
					t.Error("unexpected request", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := New(server.URL, server.Client(), func(context.Context) (string, error) { return "portal-writer", nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.UploadV2(context.Background(), V2UploadInput{RequestID: "portal-local-v1:original", Purpose: "portal.local_report", Name: "original.pdf", MediaType: "application/pdf", ResourceType: "PORTAL_REPORT_REQUEST", ResourceID: "7", BindingType: "REPORT", Content: strings.NewReader("%PDF-fixture")})
			if (err == nil) != tc.ok {
				t.Fatalf("receipt accepted=%v want=%v error=%v", err == nil, tc.ok, err)
			}
		})
	}
}
