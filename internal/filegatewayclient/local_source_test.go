package filegatewayclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLocalReportSourceNamespaceIsExact 验证HTTP来源请求显式LOCAL且未知值在本地拒绝。
// 参数：t为测试上下文。
// 返回值：无；缺来源、错误请求或未知值外发时失败。
func TestLocalReportSourceNamespaceIsExact(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input PortalReportSourceInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.SourceType != "PORTAL_LOCAL" || input.RequestID != 7 {
			t.Error("local source not explicit")
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Length", "6")
		w.Header().Set("X-File-Version-ID", "local-version")
		w.Header().Set("X-File-SHA256", strings.Repeat("a", 64))
		_, _ = io.WriteString(w, "%PDF-x")
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), func(context.Context) (string, error) { return "portal-source", nil })
	if err != nil {
		t.Fatal(err)
	}
	input := PortalReportSourceInput{SourceType: "PORTAL_LOCAL", RequestID: 7, ExpectedSHA256: strings.Repeat("a", 64), ExpectedSize: 6, ExpectedMIME: "application/pdf", ExpectedName: "local.pdf"}
	file, err := client.OpenPortalReportFile(context.Background(), "original-operation", "file-local", input)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Content.Close()
	input.SourceType = "UNKNOWN"
	if _, err := client.OpenPortalReportFile(context.Background(), "original-operation", "file-local", input); err == nil || calls != 1 {
		t.Fatal("unknown source escaped")
	}
	old, err := json.Marshal(PortalReportSourceInput{RequestID: 7})
	if err != nil || strings.Contains(string(old), "source_type") {
		t.Fatal("legacy source payload changed", string(old))
	}
}
