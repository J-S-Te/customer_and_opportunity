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

func TestPortalDelegatedReportSourceUsesExactImmutableDescriptorAndRejectsFalseEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, version, digest, media string
		want                         bool
	}{
		{"valid", "version-1", strings.Repeat("a", 64), "application/pdf", true},
		{"missing version", "", strings.Repeat("a", 64), "application/pdf", false},
		{"false digest", "version-1", strings.Repeat("b", 64), "application/pdf", false},
		{"wrong mime", "version-1", strings.Repeat("a", 64), "text/html", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v2/portal-report-files/file-1/content" || r.Header.Get("Authorization") != "Bearer portal-only-fixture" {
					t.Error("invalid source request")
				}
				var input PortalReportSourceInput
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.RequestID != 7 || input.ExpectedSHA256 != strings.Repeat("a", 64) || input.ExpectedSize != 6 || input.ExpectedMIME != "application/pdf" || input.ExpectedName != "report.pdf" {
					t.Error("source request scope missing")
				}
				w.Header().Set("Content-Type", tc.media)
				w.Header().Set("Content-Length", "6")
				w.Header().Set("X-File-Version-ID", tc.version)
				w.Header().Set("X-File-SHA256", tc.digest)
				_, _ = io.WriteString(w, "%PDF-x")
			}))
			defer server.Close()
			client, err := New(server.URL, server.Client(), func(context.Context) (string, error) { return "portal-only-fixture", nil })
			if err != nil {
				t.Fatal(err)
			}
			value, err := client.OpenPortalReportFile(context.Background(), "event-1", "file-1", PortalReportSourceInput{RequestID: 7, ExpectedSHA256: strings.Repeat("a", 64), ExpectedSize: 6, ExpectedMIME: "application/pdf", ExpectedName: "report.pdf"})
			if (err == nil) != tc.want {
				t.Fatal("false source proof accepted", err)
			}
			if tc.want {
				defer value.Content.Close()
				content, err := io.ReadAll(value.Content)
				if err != nil || string(content) != "%PDF-x" || value.VersionID != "version-1" {
					t.Fatal("invalid stream", err)
				}
			}
		})
	}
}
