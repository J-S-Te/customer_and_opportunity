package portalbootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/filing"
)

type filingExportRouteRenderer struct{}

func TestFilingExportRequiresLoginReadPermissionAndTrustedOrigin(t *testing.T) {
	config := Config{TenantID: "tenant-a", PathPrefix: "/customer-portal", PublicOrigin: "https://portal.example", SessionCookieName: "portal_session"}
	for _, tt := range []struct {
		name        string
		login       bool
		permissions []string
		origin      string
		want        int
	}{
		{"login required", false, []string{"filing.read"}, config.PublicOrigin, http.StatusUnauthorized},
		{"permission required", true, nil, config.PublicOrigin, http.StatusForbidden},
		{"foreign origin rejected", true, []string{"filing.read"}, "https://evil.example", http.StatusForbidden},
		{"valid request fails closed without renderer", true, []string{"filing.read"}, config.PublicOrigin, http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(RouterDependencies{Config: config, Account: reportRiskRouteAccountService(t, tt.permissions), Filings: filing.NewService(nil, nil, nil, nil, nil)})
			req := httptest.NewRequest(http.MethodPost, "/customer-portal/api/v1/filings/filing-public/exports", strings.NewReader(`{"expected_version":1}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("X-CSRF-Token", "1")
			if tt.login {
				req.AddCookie(&http.Cookie{Name: config.SessionCookieName, Value: "opaque-session"})
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}
