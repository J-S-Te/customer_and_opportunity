package commercial

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestReviewedPolicyAndUnknownFailClosed(t *testing.T) {
	for _, tc := range []struct {
		kind, method, path string
		want               core.Operation
	}{
		{"crm", "POST", "/api/v1/customers/duplicate-check", core.READ_HISTORY},
		{"crm", "POST", "/api/v1/customer-exports", core.EXPORT_HISTORY},
		{"crm", "POST", "/api/v1/internal/portal/invites/verify", core.ESSENTIAL_SERVICE},
		{"crm", "POST", "/api/v1/internal/portal/invites/consume", core.MUTATE_BUSINESS},
		{"portal", "GET", "/activate", core.MUTATE_BUSINESS},
		{"portal", "POST", "/api/v1/project-exports/:id/downloads", core.EXPORT_HISTORY},
		{"portal", "POST", "/internal/accounts/disable", core.ESSENTIAL_SERVICE},
		{"portal", "POST", "/internal/accounts/provision", core.MUTATE_BUSINESS},
		{"crm", "GET", "/api/v1/new-write-as-get", core.MUTATE_BUSINESS},
	} {
		if got := Operation(tc.kind, tc.method, tc.path); got != tc.want {
			t.Fatalf("%s %s got %s", tc.method, tc.path, got)
		}
	}
}

func TestMiddlewareExpiryRetainsHistoryButRejectsNewActivation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	base := r.Group("/customer-portal")
	base.Use(Middleware(func(_ context.Context, op core.Operation) error {
		if op == core.MUTATE_BUSINESS {
			return errors.New("expired")
		}
		return nil
	}, "portal", "/customer-portal"))
	for _, p := range []string{"/activate", "/api/v1/projects", "/api/v1/new-write-as-get"} {
		base.GET(p, func(c *gin.Context) { c.Status(200) })
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"/activate", 403}, {"/api/v1/projects", 200}, {"/api/v1/new-write-as-get", 403}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/customer-portal"+tc.path, nil))
		if w.Code != tc.want {
			t.Fatal(tc.path, w.Code)
		}
	}
}

func TestEnabledStartCannotFallback(t *testing.T) {
	t.Setenv("COMMERCIAL_LICENSE_ENABLED", "true")
	t.Setenv("COMMERCIAL_LICENSE_PLATFORM_PUBLIC_KEY_PATH", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Start(ctx, "customer_portal"); err == nil {
		t.Fatal("invalid controlled deployment started")
	}
}
