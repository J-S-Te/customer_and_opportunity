package portalreportreference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type readerFixture struct {
	calls           int
	tenant, account string
	customer        uint64
}

func (r *readerFixture) Resolve(_ context.Context, tenant string, customer uint64, account string) (Identity, error) {
	r.calls++
	r.tenant, r.customer, r.account = tenant, customer, account
	return Identity{TenantID: tenant, CustomerID: customer, AccountID: account, CustomerName: "Test customer", AccountMasked: "13*******90"}, nil
}
func TestReportIdentityReferenceRequiresDedicatedScopeAndPropagatesOnlySignedTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		scope, path string
		want, calls int
	}{
		{Scope, "/portal-report/download-identity?customer_id=7&account_id=subject", 200, 1},
		{"customer.summary.read", "/portal-report/download-identity?customer_id=7&account_id=subject", 403, 0},
		{Scope, "/portal-report/download-identity?customer_id=7&customer_id=8&account_id=subject", 400, 0},
		{Scope, "/portal-report/download-identity?customer_id=7&account_id=subject&account_id=another", 400, 0},
	} {
		reader := &readerFixture{}
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{TenantID: "signed-tenant", UserID: "machine:portal-reader", Permissions: map[string]struct{}{tc.scope: {}}}))
			c.Next()
		})
		RegisterRoutes(router.Group(""), reader)
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("X-Tenant-ID", "forged-tenant")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.want || reader.calls != tc.calls {
			t.Fatal(response.Code, reader.calls, response.Body.String())
		}
		if tc.calls == 1 && (reader.tenant != "signed-tenant" || reader.customer != 7 || reader.account != "subject" || strings.Contains(response.Body.String(), "13800000090") || response.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("invalid bounded identity response", response.Body.String())
		}
	}
}
func TestLoginIdentityMaskNeverReturnsWholeAccount(t *testing.T) {
	for _, account := range []string{"1", "abcd", "13800000090", "ext-user@example.com"} {
		masked := mask(account)
		if masked == account || !strings.Contains(masked, "*") {
			t.Fatal("account not masked")
		}
	}
}

func TestReportIdentityReferenceNilProviderFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{TenantID: "tenant", Permissions: map[string]struct{}{Scope: {}}}))
		c.Next()
	})
	RegisterRoutes(router.Group(""), nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/portal-report/download-identity?customer_id=1&account_id=subject", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil identity provider failed open: %d", res.Code)
	}
}
