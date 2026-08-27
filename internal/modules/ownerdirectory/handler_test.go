package ownerdirectory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type catalogStub struct {
	query Query
	page  Page
	err   error
	calls int
}

func TestOwnerDirectoryHandlerFailsClosedWhenCatalogIsNil(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/owner-directory", nil)

	NewHandler(nil).List(context)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "CRM_OWNER_DIRECTORY_UNAVAILABLE") {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func (stub *catalogStub) List(_ context.Context, query Query) (Page, error) {
	stub.calls++
	stub.query = query
	return stub.page, stub.err
}

func (stub *catalogStub) Validate(context.Context, string, string) error { return stub.err }
func (stub *catalogStub) Resolve(context.Context, []string) (map[string]User, error) {
	return nil, stub.err
}

func TestOwnerDirectoryHandlerRejectsInvalidPageBeforeCallingCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	catalog := &catalogStub{}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/owner-directory?page=0", nil)

	NewHandler(catalog).List(context)

	if context.Writer.Status() != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want=%d", context.Writer.Status(), http.StatusUnprocessableEntity)
	}
	if catalog.calls != 0 {
		t.Fatalf("catalog calls=%d want=0", catalog.calls)
	}
}

func TestOwnerDirectoryHandlerFailsClosedWhenPlatformIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	catalog := &catalogStub{err: errors.New("gateway timeout")}
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/owner-directory?keyword=%E5%BC%A0%E4%B8%89", nil)

	NewHandler(catalog).List(context)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "CRM_OWNER_DIRECTORY_UNAVAILABLE") {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestOwnerDirectoryRouteAllowsExactlyOneSupportedBusinessCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	permissions := []string{"customer.read", "customer.create", "customer.update", "opportunity.create", "opportunity.owner.change", "opportunity.team.manage", "presale.report"}
	for _, permission := range permissions {
		t.Run(permission, func(t *testing.T) {
			catalog := &catalogStub{page: Page{Items: []User{}, Page: 1, PageSize: 20}}
			response := serveDirectoryRoute(catalog, map[string]struct{}{permission: {}})
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if catalog.calls != 1 {
				t.Fatalf("catalog calls=%d want=1", catalog.calls)
			}
		})
	}
}

func TestOwnerDirectoryRouteRejectsUnrelatedCapability(t *testing.T) {
	catalog := &catalogStub{}
	response := serveDirectoryRoute(catalog, map[string]struct{}{"customer.audit.read": {}})
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if catalog.calls != 0 {
		t.Fatalf("catalog calls=%d want=0", catalog.calls)
	}
}

func serveDirectoryRoute(catalog Catalog, permissions map[string]struct{}) *httptest.ResponseRecorder {
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		principal := auth.Principal{TenantID: "tenant-a", UserID: "actor-a", Permissions: permissions, ScopeMode: auth.ScopeAll}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	RegisterRoutes(group, NewHandler(catalog))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/owner-directory", nil))
	return response
}
